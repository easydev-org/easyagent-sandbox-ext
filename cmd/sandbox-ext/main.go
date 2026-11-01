// Command sandbox-ext is the easyagent sandbox + GUI-automation extension
// server — the Go rebrand of the worker-extension. It drives easyworker
// sandboxes (the worker.v1 Connect contract) for shell/file/job operations and,
// where the accessibility CLI is present, native GUI automation.
package main

import (
	_ "embed"
	"context"
	"log/slog"
	"net/http"
	"os"

	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"
	"github.com/easydev-org/easybus-sdk/manifest"
	"github.com/easydev-org/easybus-sdk/transport/nats"

	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/tools"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/uiapi"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/config"
)

//go:embed manifest.yaml
var manifestYAML []byte

// uiBase is where this extension's API is mounted.
//
// IT IS NO LONGER A MOUNT FOR A PANEL. The panel is a separate project
// (easydev-org/easyagent-sandbox-ext-ui), installed and served by easyagent-ext-ui-host; this binary
// is a backend and has no assets of its own. The constant survives because the API's paths are
// absolute — /ext/sandbox/api/... — and the host forwards exactly those here.
const uiBase = "/ext/sandbox"

// uiMux builds the panel's API surface.
func uiMux(cache *sandbox.ClientCache, sandboxes func(session, tenant string) []config.Sandbox) http.Handler {
	// The mount path, not a relative one: MountUI hands the handler the request it received, so the
	// routes have to carry the prefix.
	return uiapi.Handler(uiBase+"/api", uiapi.Deps{
		Sandboxes: sandboxes,
		Cache:     cache,
		// The agent owns the token table; an extension cannot verify a bearer on its own.
		Tenants: &uiapi.AgentIdentity{AgentURL: envOr("AGENT_URL", "http://agent.dev.svc.cluster.local")},
	})
}

func main() {
	log := slog.Default().With("svc", "sandbox-ext")

	natsURL := envOr("NATS_URL", "nats://127.0.0.1:4222")
	bus, err := nats.Connect(natsURL)
	if err != nil {
		log.Error("nats connect failed", "err", err)
		os.Exit(1)
	}

	m, err := manifest.ParseManifest(manifestYAML)
	if err != nil {
		log.Error("load manifest failed", "err", err)
		os.Exit(1)
	}

	var ext *extension.Extension
	cache := sandbox.NewClientCache(16)
	handlers := tools.Registry(tools.Deps{
		Cache: cache,
		Bus:   bus,
		GetConfig: func(name, session, tenant string) any {
			if ext == nil {
				return nil
			}
			return ext.GetConfig(tenant, name, session)
		},
		Locale: func(session, tenant string) string {
			if ext == nil {
				return ""
			}
			return ext.GetSessionVariable(context.Background(), tenant, "agent", session, "locale", "")
		},
	})

	specs := make(map[string]extension.ToolSpec, len(handlers))
	for name, h := range handlers {
		h := h
		specs[name] = extension.ToolSpec{
			Execute: func(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
				// callID used to be dropped here. It is the only handle that ties a
				// long-running command's progress back to the caller watching THIS
				// call, so it has to reach the handler.
				//
				// The tenant is known HERE, which is why the progress sink is
				// installed per call instead of once in Deps: ReportProgress needs
				// both the call id and the tenant to address
				// easybus.<tenant>.tool.progress.<callID>.
				cctx := tools.WithProgress(ctx, func(pctx context.Context, p *easybus.ToolProgress) {
					if ext != nil {
						// A dropped UI update must never fail the tool: the command
						// already ran.
						_ = ext.ReportProgress(pctx, tenant, callID, p)
					}
				})
				return h(cctx, args, callID, session, tenant)
			},
		}
	}

	cfg := m.BuildConfig(manifest.Bindings{
		Handlers: specs,
		OnLifecycle: func(ctx context.Context, ev *easybus.LifecycleEvent, tenant string) error {
			log.Info("lifecycle", "kind", easybus.LifecycleKindString(ev.Kind), "tenant", tenant, "session", ev.SessionName)
			return nil
		},
	})
	ext = extension.New(bus, cfg)

	if err := extension.Serve(ext, extension.ServeOptions{
		// NO UI SPEC, AND THAT IS THE DESIGN. The panel is easyagent-ext-ui-host's to serve: it installs
		// the bundle from a release and answers the console's discovery probe from that bundle's own
		// ui.json. A UISpec here would be a second, competing answer — and the console reads whichever
		// it asks, so the two would disagree the first time either changed.
		//
		// IT WOULD ALSO NOT WORK. MountUI returns the handler untouched when spec.Assets is nil, so a
		// UISpec without assets is ignored IN FULL — including APIPrefix. The API below does not need
		// it: its routes carry the whole path (/ext/sandbox/api/...) because they are served by a
		// different process at the other end of a proxy, not by a mount that strips a prefix.
		Handler: uiMux(cache, func(session, tenant string) []config.Sandbox {
			if ext == nil {
				return nil
			}
			raw, _ := ext.GetConfig(tenant, config.KnobSandboxes, session).(string)
			return config.Parse(raw)
		}),
		Run: func(ctx context.Context, _ *extension.Extension) {
			log.Info("listening", "nats", natsURL)
			// Stop relaying job output once the process is going away, so no
			// watcher goroutine outlives the extension. Serve returns when ctx is
			// cancelled; closing here covers the shutdown path.
			go func() {
				<-ctx.Done()
				tools.CloseWatchers()
			}()
		},
	}); err != nil {
		log.Error("serve failed", "err", err)
		os.Exit(1)
	}
	// Serve returned: also release any watcher still holding a worker stream.
	tools.CloseWatchers()
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
