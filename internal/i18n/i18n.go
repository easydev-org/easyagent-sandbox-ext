// Package i18n localises the sandbox-ext RUNTIME tool text (content + errors)
// using the easybus SDK's catalog core.
package i18n

import (
	easybusi18n "github.com/easydev-org/easybus-sdk/i18n"
)

// Message keys (typed; a typo is a compile error at the call site).
const (
	MsgNotConfigured = "notConfigured"
	MsgUnknownTarget = "unknownTarget"
	MsgJobStarted    = "jobStarted"
	MsgJobStill      = "jobStillRunning"
	MsgJobDone       = "jobDone"
	MsgNoSandboxes   = "noSandboxes"
	MsgUploaded      = "uploaded"
	MsgDownloaded    = "downloaded"
)

// Catalog is the message table (open locale set: add a column per language).
var Catalog = easybusi18n.Catalog{
	MsgNotConfigured: {
		"en": "no sandboxes configured — set the `{name}` config first",
		"zh": "尚未配置沙箱 —— 请先设置 `{name}` 配置。",
	},
	MsgUnknownTarget: {
		"en": "unknown sandbox {target} (known: {known})",
		"zh": "未知的沙箱 {target}（已知：{known}）",
	},
	MsgJobStarted: {
		"en": "started job {id}",
		"zh": "已启动任务 {id}",
	},
	MsgJobStill: {
		"en": "job {id} still running after {timeout}s; use job-output/job-wait",
		"zh": "任务 {id} 在 {timeout}s 后仍在运行；请用 job-output/job-wait",
	},
	MsgJobDone: {
		"en": "job {id} {state} (exit {code})",
		"zh": "任务 {id} {state}（退出码 {code}）",
	},
	MsgNoSandboxes: {
		"en": "no sandboxes configured",
		"zh": "未配置沙箱",
	},
	MsgUploaded: {
		"en": "uploaded {n} file(s) as file:{code}",
		"zh": "已上传 {n} 个文件为 file:{code}",
	},
	MsgDownloaded: {
		"en": "downloaded file:{code} to {path}",
		"zh": "已将 file:{code} 下载到 {path}",
	},
}

// Params are the `{placeholder}` interpolation values.
type Params = easybusi18n.Params

var tr = easybusi18n.New(Catalog, "")

// T resolves a message key for a locale (” => the base locale).
func T(locale, key string, params Params) string {
	return tr.T(locale, key, params)
}
