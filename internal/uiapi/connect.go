package uiapi

import (
	"context"
	"errors"
	"io"
	"strings"

	"connectrpc.com/connect/v2"

	uiv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/ui/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/gen/ui/v1/uiv1connect"
	workerv1 "github.com/easydev-org/easyagent-sandbox-ext/gen/worker/v1"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/config"
	"github.com/easydev-org/easyagent-sandbox-ext/internal/sandbox"
)

/**
 * The panel's data API, as a Connect service.
 *
 * THIS REPLACES four hand-written JSON routes and one SSE stream. The routes worked; what they did not
 * do is state their contract — their shapes were `map[string]any` literals written at the point of
 * emission, so the panel's contract was a side effect of whatever the handler happened to emit. That
 * arrangement has already produced an empty file tree in another extension, where a route emitted
 * `Path` and the panel read `path`.
 *
 * THE SCREEN STREAM IS NOT HERE, deliberately. /ext/sandbox/api/sandboxes/{name}/screen stays a
 * WebSocket proxying H.264 from easystream: protobuf-wrapping video frames would add an encode and a
 * decode per frame and make the panel reassemble frame boundaries itself. A media stream wants a
 * WebSocket, and it has one. See the note in proto/ui/v1/ui.proto.
 */
type Service struct {
	// Sandboxes reports the caller's configured machines. The same source the tools use.
	Sandboxes func(session, tenant string) []config.Sandbox
	// Cache hands out per-worker clients, so a panel and a tool call share one connection per machine.
	Cache *sandbox.ClientCache
	// Tenants resolves a bearer to a tenant. Required: without it every request is refused.
	Tenants IdentityResolver
	// SignKey signs the short-lived URLs the desktop proxy accepts. Optional: when empty a key is
	// derived from a constant, which is stable across restarts but is NOT a secret against anybody who
	// can read this source. A deployment that cares should configure one.
	SignKey []byte
}

var _ uiv1connect.UiServiceHandler = (*Service)(nil)

// NewServer builds the Connect server for this service, with authentication installed.
func (s *Service) NewServer() *connect.Server {
	server := connect.NewServer(authInterceptor(s))
	uiv1connect.RegisterUiServiceHandler(server, s)
	return server
}

/**
 * authInterceptor resolves the caller once, for every RPC.
 *
 * THE TENANT COMES FROM THE TOKEN, NEVER FROM THE REQUEST. The session a caller passes selects among
 * their OWN machines; it cannot widen the scope, and a tenant in a message body would be one they
 * chose. That is why this is an interceptor rather than a check each method is trusted to make: a
 * method added later cannot forget it, and the failure mode of forgetting is showing one tenant
 * another's machines.
 */
func authInterceptor(s *Service) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			if s.Tenants == nil {
				// Failing closed: a deployment that forgot to wire identity must not serve data.
				return connect.NewError(connect.CodeInternal, "identity is not configured")
			}
			info, ok := connect.CallInfoForServerContext(ctx)
			if !ok {
				return connect.NewError(connect.CodeInternal, "no call info on the request context")
			}
			bearer := bearerOfHeader(info.RequestHeader().Get("Authorization"))
			if bearer == "" {
				return connect.NewError(connect.CodeUnauthenticated, ErrUnauthenticated.Error())
			}
			tenant, err := s.Tenants.Tenant(ctx, bearer)
			if err != nil {
				if errors.Is(err, ErrUnauthenticated) {
					return connect.NewError(connect.CodeUnauthenticated, err.Error())
				}
				// A failure to REACH the agent is not an authentication failure: answering
				// UNAUTHENTICATED would log the reader out over a momentary blip.
				return connect.NewError(connect.CodeUnavailable, err.Error())
			}
			return next(context.WithValue(ctx, tenantKey{}, tenant), spec, stream)
		}
	}
}

type tenantKey struct{}

func tenantFrom(ctx context.Context) (string, error) {
	t, ok := ctx.Value(tenantKey{}).(string)
	if !ok || t == "" {
		return "", connect.NewError(connect.CodeInternal, "no tenant on the request context")
	}
	return t, nil
}

// bearerOfHeader reads an Authorization header.
func bearerOfHeader(v string) string {
	if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}

// find resolves a named sandbox within the caller's scope.
//
// NOT_FOUND RATHER THAN PERMISSION_DENIED for a name that is not theirs: the caller is allowed to know
// that THIS NAME is not one of theirs, and the alternative would tell them it exists somewhere else.
func (s *Service) find(ctx context.Context, session, name string) (config.Sandbox, error) {
	tenant, err := tenantFrom(ctx)
	if err != nil {
		return config.Sandbox{}, err
	}
	if name == "" {
		return config.Sandbox{}, connect.NewError(connect.CodeInvalidArgument, "sandbox is required")
	}
	for _, sb := range s.Sandboxes(session, tenant) {
		if sb.Name == name {
			return sb, nil
		}
	}
	return config.Sandbox{}, connect.NewError(connect.CodeNotFound, "no such sandbox: "+name)
}

// workerErr maps a worker's Connect error onto a code the panel can act on.
//
// A WORKER THAT IS MERELY UNREACHABLE IS UNAVAILABLE, not INTERNAL: it is a transient state of a live
// resource, and the panel retries rather than showing a permanent failure.
func workerErr(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeNotFound:
		return connect.NewError(connect.CodeNotFound, err.Error())
	case connect.CodeInvalidArgument:
		return connect.NewError(connect.CodeInvalidArgument, err.Error())
	case connect.CodeUnauthenticated, connect.CodePermissionDenied:
		return connect.NewError(connect.CodePermissionDenied, err.Error())
	case connect.CodeUnavailable:
		return connect.NewError(connect.CodeUnavailable, err.Error())
	default:
		return connect.NewError(connect.CodeInternal, err.Error())
	}
}

// ListSandboxes reports the caller's machines and whether each one answered.
func (s *Service) ListSandboxes(ctx context.Context, req *uiv1.ListSandboxesRequest) (*uiv1.ListSandboxesResponse, error) {
	tenant, err := tenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	list := s.Sandboxes(req.GetSession(), tenant)
	out := make([]*uiv1.Sandbox, 0, len(list))
	for _, sb := range list {
		row := &uiv1.Sandbox{Name: sb.Name}
		// ONE PROBE PER SANDBOX, in sequence: a tenant has a handful, and a parallel fan-out would let
		// one slow worker hold the whole list while complicating cancellation for no gain.
		info, ierr := s.Cache.Worker(sandbox.Endpoint{URL: sb.URL, Token: sb.Token}).
			Info(ctx, &workerv1.InfoRequest{})
		if ierr != nil {
			// A SANDBOX THAT IS DOWN IS INFORMATION. Reporting the error rather than omitting the row
			// is what lets the panel show "unreachable" instead of quietly looking empty.
			row.Error = ierr.Error()
		} else {
			row.Live = true
			row.Os = info.GetOs()
			row.Platform = info.GetCapabilities().GetPlatform()
			row.Screen = info.GetCapabilities().GetDesktop()
			row.A11Y = info.GetCapabilities().GetXa11Y()
		}
		out = append(out, row)
	}
	return &uiv1.ListSandboxesResponse{Sandboxes: out, Count: int32(len(out))}, nil
}

// ListJobs asks one machine's daemon what it has run.
func (s *Service) ListJobs(ctx context.Context, req *uiv1.ListJobsRequest) (*uiv1.ListJobsResponse, error) {
	sb, err := s.find(ctx, req.GetSession(), req.GetSandbox())
	if err != nil {
		return nil, err
	}
	limit := req.GetLimit()
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	res, lerr := s.Cache.Worker(sandbox.Endpoint{URL: sb.URL, Token: sb.Token}).
		ListJobs(ctx, &workerv1.ListJobsRequest{Limit: limit})
	if lerr != nil {
		return nil, workerErr(lerr)
	}
	entries := res.GetJobs()
	out := make([]*uiv1.Job, 0, len(entries))
	for _, j := range entries {
		out = append(out, &uiv1.Job{
			Id: j.GetId(), Command: j.GetCommand(), State: j.GetState(),
			ExitCode: j.GetExitCode(), StartedAt: j.GetStartedAt(), FinishedAt: j.GetFinishedAt(),
			// FILLED IN HERE because the worker cannot: it was asked, so it does not know which of the
			// caller's machines the answer came from.
			Sandbox: sb.Name, Namespace: j.GetNamespace(), Lang: j.GetLang(),
		})
	}
	return &uiv1.ListJobsResponse{Jobs: out, Count: int32(len(out))}, nil
}

// GetJobOutput returns one window of a job's output.
func (s *Service) GetJobOutput(ctx context.Context, req *uiv1.GetJobOutputRequest) (*uiv1.GetJobOutputResponse, error) {
	sb, err := s.find(ctx, req.GetSession(), req.GetSandbox())
	if err != nil {
		return nil, err
	}
	if req.GetJobId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "job_id is required")
	}
	res, oerr := s.Cache.Worker(sandbox.Endpoint{URL: sb.URL, Token: sb.Token}).
		JobOutput(ctx, &workerv1.JobOutputRequest{JobId: req.GetJobId(), Start: req.GetStart()})
	if oerr != nil {
		return nil, workerErr(oerr)
	}
	return &uiv1.GetJobOutputResponse{
		Lines:      res.GetLines(),
		TotalLines: res.GetTotalLines(),
		StartLine:  res.GetStartLine(),
		EndLine:    res.GetEndLine(),
		Done:       res.GetDone(),
	}, nil
}

/**
 * WatchJob streams a job's output until it finishes.
 *
 * THIS USED TO BE SERVER-SENT EVENTS, and the reason was stated plainly in the old code: "the browser
 * cannot speak Connect". It can now — the panel uses the same generated client as every other call —
 * so the stream is a server-streaming RPC on the same transport, with the same authentication, and no
 * second way to be authorised.
 *
 * THE UPSTREAM IS STILL THE WORKER'S OWN STREAM. This is a relay, not a poll: the daemon pushes, and
 * every message is forwarded as it arrives.
 */
func (s *Service) WatchJob(ctx context.Context, req *uiv1.WatchJobRequest, stream uiv1connect.UiServiceWatchJobServerStream) error {
	sb, err := s.find(ctx, req.GetSession(), req.GetSandbox())
	if err != nil {
		return err
	}
	if req.GetJobId() == "" {
		return connect.NewError(connect.CodeInvalidArgument, "job_id is required")
	}

	up, werr := s.Cache.Worker(sandbox.Endpoint{URL: sb.URL, Token: sb.Token}).
		WatchJob(ctx, &workerv1.WatchJobRequest{JobId: req.GetJobId()})
	if werr != nil {
		return workerErr(werr)
	}
	defer func() { _ = up.Close() }()

	for {
		msg, rerr := up.Receive()
		if rerr != nil {
			// io.EOF is the normal end of a server stream. Anything else is worth telling the reader
			// about — unless they are the ones who hung up, in which case the context says so and an
			// error would be noise on a closed connection.
			if errors.Is(rerr, io.EOF) || ctx.Err() != nil {
				return nil
			}
			// SENT AS AN EVENT RATHER THAN RETURNED: the caller may already have received output, and
			// failing the whole RPC would discard a stream they are reading. A trailing error event
			// says what happened after what they saw.
			_ = stream.Send(&uiv1.WatchJobResponse{
				Event: &uiv1.WatchJobResponse_Error{Error: rerr.Error()},
			})
			return nil
		}
		switch ev := msg.GetEvent().(type) {
		case *workerv1.WatchJobResponse_Output:
			if err := stream.Send(&uiv1.WatchJobResponse{
				Event: &uiv1.WatchJobResponse_Output{Output: ev.Output},
			}); err != nil {
				return err
			}
		case *workerv1.WatchJobResponse_Done_:
			// THE TERMINAL EVENT, always last. A structured message rather than JSON inside a string:
			// the old SSE frame made the reader parse a payload to find an exit code.
			return stream.Send(&uiv1.WatchJobResponse{
				Event: &uiv1.WatchJobResponse_Done{Done: &uiv1.JobDone{
					ExitCode: ev.Done.GetExitCode(),
					Stdout:   ev.Done.GetStdout(),
					Stderr:   ev.Done.GetStderr(),
				}},
			})
		}
	}
}
