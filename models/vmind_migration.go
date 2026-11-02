package models

// MigrationVMindID 仅用于 2026-11-02 三地滚动改名，避免旧配置/旧调用方在更新窗口断流。
// 三地配置与两套调用方切换并验收后，本次发布撤掉该函数及请求侧入口；不作为长期模型别名。
func MigrationVMindID(model string) string {
	switch model {
	case "VWorld_wardrobe-1.0":
		return VWorldWardrobe10
	case "VWorld_outfit_1.0":
		return VWorldOutfit10
	case "VWorld_bodymodel_1.0":
		return VWorldBodyModel10
	case "VWorld_outfit_video_1.0":
		return VWorldOutfitVideo10
	case "segment-person-bria":
		return SegmentPersonBria
	case "segment-subject-bria":
		return SegmentSubjectBria
	case "human_parse":
		return HumanParse
	default:
		return model
	}
}
