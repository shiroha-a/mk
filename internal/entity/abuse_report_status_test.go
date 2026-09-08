package entity

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/core/notification"
)

func abuseNotification() *notification.Notification {
	return &notification.Notification{
		ID: "n1", Type: notification.TypeAbuseReport, NotifierID: "reporter",
		Extra: map[string]any{"reportId": "r1", "targetUserId": "u2"},
	}
}

// #2868: 未対応の通報は resolved=false で返る。
func TestPackNotification_AbuseReportUnresolved(t *testing.T) {
	out := PackNotification(abuseNotification(), nil, nil, nil, nil, nil,
		WithAbuseReportLookup(func(id string) (AbuseReportStatus, bool) {
			require.Equal(t, "r1", id)
			return AbuseReportStatus{}, true
		}))
	require.NotNil(t, out)
	require.Equal(t, false, out["resolved"])
	require.NotContains(t, out, "resolvedAs")
	require.NotContains(t, out, "assigneeId")
}

// **対処済みは read 時に分かる (#2868)。** 通知は作成時点の状態しか持たないので、
// 他のモデレーターが対処しても通知欄が「未対応」のまま残ってしまう。
func TestPackNotification_AbuseReportResolved(t *testing.T) {
	out := PackNotification(abuseNotification(), nil, nil, nil, nil, nil,
		WithAbuseReportLookup(func(string) (AbuseReportStatus, bool) {
			return AbuseReportStatus{Resolved: true, ResolvedAs: "accept", AssigneeID: "mod1"}, true
		}))
	require.NotNil(t, out)
	require.Equal(t, true, out["resolved"])
	require.Equal(t, "accept", out["resolvedAs"])
	require.Equal(t, "mod1", out["assigneeId"])
}

// 通報が消えていたら通知ごと drop する (roleAssigned と同じ形)。
func TestPackNotification_AbuseReportDeletedIsDropped(t *testing.T) {
	out := PackNotification(abuseNotification(), nil, nil, nil, nil, nil,
		WithAbuseReportLookup(func(string) (AbuseReportStatus, bool) {
			return AbuseReportStatus{}, false
		}))
	require.Nil(t, out, "削除済みの通報を指す通知は返さない")
}

// lookup 未配線でも drop する (fail-closed)。
//
// 状態を出せないまま「未対応」に見せると、対処済みの通報に別のモデレーターが
// 二重で当たる。
func TestPackNotification_AbuseReportWithoutLookupIsDropped(t *testing.T) {
	require.Nil(t, PackNotification(abuseNotification(), nil, nil, nil, nil, nil))
}

// reportId を raw で surface しない (roleId / invitationId と同じ)。
func TestPackNotification_AbuseReportKeepsReportID(t *testing.T) {
	out := PackNotification(abuseNotification(), nil, nil, nil, nil, nil,
		WithAbuseReportLookup(func(string) (AbuseReportStatus, bool) {
			return AbuseReportStatus{}, true
		}))
	require.NotNil(t, out)
	// **reportId は残す。** frontend が「通報を確認」ボタンの遷移先に使う。
	require.Equal(t, "r1", out["reportId"])
	require.Equal(t, "u2", out["targetUserId"])
}
