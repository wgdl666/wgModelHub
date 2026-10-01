package provider

// VideoJobState 是供应商侧单次查询结果；对外 RPC 状态由 service 结合本地表再映射。
type VideoJobState int

const (
	VideoJobRunning VideoJobState = iota
	VideoJobSucceeded
	VideoJobFailed
)

// VideoJob 是 GetVideo 的归一化快照；Err 仅在 Failed 时有意义。
type VideoJob struct {
	State       VideoJobState
	PollAfterMs int32
	Err         error
}
