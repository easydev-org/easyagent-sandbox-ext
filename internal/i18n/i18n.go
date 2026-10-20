// Package i18n localises the sandbox-ext RUNTIME tool text (content + errors)
// using the easybus SDK's catalog core.
package i18n

import (
	easybusi18n "github.com/easydev-org/easybus-sdk/i18n"
)

// Message keys (typed; a typo is a compile error at the call site).
const (
	MsgNotConfigured          = "notConfigured"
	MsgUnknownTarget          = "unknownTarget"
	MsgJobStarted             = "jobStarted"
	MsgJobStill               = "jobStillRunning"
	MsgJobDone                = "jobDone"
	MsgNoSandboxes            = "noSandboxes"
	MsgUploaded               = "uploaded"
	MsgDownloaded             = "downloaded"
	MsgJobGraphHead           = "jobGraphHead"
	MsgJobGraphEmpty          = "jobGraphEmpty"
	MsgJobGraphHint           = "jobGraphHint"
	MsgJobGraphFolded         = "jobGraphFolded"
	MsgJobGraphDataEdges      = "jobGraphDataEdges"
	MsgJobGraphInheritedEdges = "jobGraphInheritedEdges"
	MsgJobGraphNoDataEdges    = "jobGraphNoDataEdges"
	MsgJobFlowHead            = "jobFlowHead"
	MsgJobFlowEmpty           = "jobFlowEmpty"
	MsgJobFlowMore            = "jobFlowMore"
	MsgJobFlowFiltered        = "jobFlowFiltered"
	MsgJobFlowNextPage        = "jobFlowNextPage"
	MsgJobStdinOK             = "jobStdinOK"
	MsgIntermediatesHead      = "intermediatesHead"
	MsgIntermediatesCut       = "intermediatesCut"
	MsgIntermediatesMore      = "intermediatesMore"
	MsgIntermediatesHint      = "intermediatesHint"
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
	MsgJobGraphHead: {
		"en": "=== dataflow of job {id}: {n} stream(s), {e} edge(s) ===",
		"zh": "=== 任务 {id} 的数据流：{n} 个流，{e} 条边 ===",
	},
	MsgJobGraphEmpty: {
		"en": "no streams recorded for this job (it may not have started, or the worker was restarted)",
		"zh": "该任务没有记录任何流（可能尚未启动，或 worker 已重启）",
	},
	MsgJobGraphHint: {
		"en": "read one stream's content with sandbox-job-flow {job-id, fid}",
		"zh": "用 sandbox-job-flow {job-id, fid} 查看某个流的内容",
	},
	MsgJobGraphFolded: {
		"en": "{n} fd endpoint(s) folded — a stage's stdin/stdout/stderr identity, carrying no bytes (edges above still include them). Pass all: true to list them.",
		"zh": "已折叠 {n} 个 fd 端点——它们是各阶段的 stdin/stdout/stderr 标识，本身不含字节（上面的边仍包含它们）。传 all: true 可列出。",
	},
	MsgJobGraphDataEdges: {
		"en": "data paths (output of one command became input of another):",
		"zh": "数据通路（一个命令的输出成为另一个命令的输入）：",
	},
	MsgJobGraphInheritedEdges: {
		"en": "{n} inherited edge(s) folded — a stage's stdin/stderr pointing at the job's own, which every stage has and which shows nothing about the work.",
		"zh": "已折叠 {n} 条继承边——各阶段的 stdin/stderr 指向任务自身的 fd，每个阶段都有，不体现任务实际做了什么。",
	},
	MsgJobGraphNoDataEdges: {
		"en": "no data paths: nothing was piped or redirected, so the stages are unconnected.",
		"zh": "没有数据通路：没有管道或重定向，各阶段彼此不连通。",
	},
	MsgJobFlowHead: {
		"en": "=== flow fid {fid}: {total} byte(s) ===",
		"zh": "=== 流 fid {fid}：共 {total} 字节 ===",
	},
	MsgJobFlowEmpty: {
		"en": "(this stream is empty)",
		"zh": "（该流为空）",
	},
	MsgJobFlowMore: {
		"en": "... truncated: {next} of {total} bytes shown; continue with offset={next}",
		"zh": "……已截断：显示 {total} 字节中的 {next} 字节；用 offset={next} 继续",
	},
	MsgJobFlowFiltered: {
		"en": "filtered by /{pattern}/: {n} of {total} line(s) match",
		"zh": "按 /{pattern}/ 过滤：{total} 行中有 {n} 行匹配",
	},
	MsgJobFlowNextPage: {
		"en": "more lines available: re-run with offset={next} (limit up to {max})",
		"zh": "还有更多行：用 offset={next} 继续（limit 上限 {max}）",
	},
	MsgJobStdinOK: {
		"en": "wrote {n} byte(s) to job {id} stdin (closed={closed})",
		"zh": "向任务 {id} 的标准输入写入 {n} 字节（close={closed}）",
	},
	MsgIntermediatesHead: {
		"en": "\n--- intermediate results ({n} stream(s) that never reached stdout) ---",
		"zh": "\n--- 中间结果（{n} 个未到达 stdout 的流）---",
	},
	MsgIntermediatesCut: {
		"en": "... (truncated; this stream is {bytes} bytes — use sandbox-job-flow to page it)",
		"zh": "……（已截断；该流共 {bytes} 字节 —— 用 sandbox-job-flow 分页读取）",
	},
	MsgIntermediatesMore: {
		"en": "... {n} more stream(s) omitted — see sandbox-job-graph",
		"zh": "……还有 {n} 个流未显示 —— 见 sandbox-job-graph",
	},
	MsgIntermediatesHint: {
		"en": "these are what each pipeline stage produced; the stdout above is only the last stage",
		"zh": "以上是每个管道阶段产生的数据；上面的 stdout 只是最后一段",
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
