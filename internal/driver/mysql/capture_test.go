package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capture"
)

// readable is a server whose binary log a reader can stream, as the given user.
func readable(privileges ...string) captureAnswer {
	return captureAnswer{logBin: true, format: "ROW", image: "FULL", privileges: privileges}
}

func TestABinlogReaderIsPermittedOnlyWithEverySettingAndPrivilegeInPlace(t *testing.T) {
	for name, a := range map[string]captureAnswer{
		"mysql":   readable(privReplicationSlave, privReplicationClient),
		"mariadb": readable(privReplicationSlave, privBinlogMonitor),
	} {
		if got := a.capture(); !got.Permitted {
			t.Errorf("%s: a readable server was refused: %+v", name, got)
		}
	}

	for name, tc := range map[string]struct {
		answer captureAnswer
		names  string
	}{
		"log_bin": {captureAnswer{format: "ROW", image: "FULL", privileges: readable(privReplicationSlave, privReplicationClient).privileges}, "log_bin = ON"},
		"format":  {captureAnswer{logBin: true, format: "MIXED", image: "FULL"}, "binlog_format = ROW (it is MIXED)"},
		"image":   {captureAnswer{logBin: true, format: "ROW", image: "MINIMAL"}, "binlog_row_image = FULL (it is MINIMAL)"},
		"row values": {captureAnswer{logBin: true, format: "ROW", image: "FULL", rowValues: "PARTIAL_JSON"},
			"binlog_row_value_options empty, so every update carries its whole row (it is PARTIAL_JSON)"},
		"slave":     {readable(privReplicationClient), "REPLICATION SLAVE"},
		"client":    {readable(privReplicationSlave), "REPLICATION CLIENT"},
		"unrelated": {readable("PROCESS"), "REPLICATION SLAVE"},
	} {
		t.Run(name, func(t *testing.T) {
			got := tc.answer.capture()
			if got.Permitted || !strings.Contains(got.Reason(), tc.names) {
				t.Errorf("the answer %+v was read as %+v, want a refusal naming %q", tc.answer, got, tc.names)
			}
		})
	}
}

func TestCaptureRefusesARequestItCannotServeBeforeConnecting(t *testing.T) {
	for name, tc := range map[string]struct {
		req  capture.Request
		says string
	}{
		"no resource": {capture.Request{Tables: []string{"people"}}, "names no resource"},
		"no table":    {capture.Request{Name: "migr_x"}, "names no table"},
		"two tables":  {capture.Request{Name: "migr_x", Tables: []string{"people", "pets"}}, "one table's changes per stream"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := (&mysqlDriver{}).Capture(context.Background(), "nowhere", tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("Capture returned %v, want a refusal saying %q", err, tc.says)
			}
		})
	}

	_, err := (&mysqlDriver{}).Capture(context.Background(), "", capture.Request{Name: "migr_x", Tables: []string{"people"}})
	if !errors.As(err, new(*DSNError)) {
		t.Errorf("Capture over an empty connection string returned %v, want the DSN refusal", err)
	}
}

func TestAPermittedServerSaysHowLongItKeepsItsLog(t *testing.T) {
	for expires, says := range map[int64]string{
		2592000: "binlog_expire_logs_seconds = 2592000, so a binary log file is purged 720h0m0s after",
		0:       "no binary log is purged by age",
		-1:      "",
	} {
		a := readable(privReplicationSlave, privReplicationClient)
		a.expires = expires

		got := a.capture()
		if !got.Permitted || !strings.Contains(got.Retention, says) || (says == "" && got.Retention != "") {
			t.Errorf("binlog_expire_logs_seconds %d was read as %+v, want it saying %q", expires, got, says)
		}
	}
}
