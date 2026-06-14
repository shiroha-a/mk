package model

// SuspendedSoftwareEntry is one entry of meta.deliverSuspendedSoftware:
// federation delivery to instances running the matching software (+ version
// range) is suspended. Misskey TS の SoftwareSuspension に対応。マッチ判定は
// core/federation.MatchSuspendedSoftware が行う (#1732)。
type SuspendedSoftwareEntry struct {
	Software     string `json:"software"`
	VersionRange string `json:"versionRange"`
}
