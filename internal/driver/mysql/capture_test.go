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
		"log_bin":   {captureAnswer{format: "ROW", image: "FULL", privileges: readable(privReplicationSlave, privReplicationClient).privileges}, "log_bin = ON"},
		"format":    {captureAnswer{logBin: true, format: "MIXED", image: "FULL"}, "binlog_format = ROW (it is MIXED)"},
		"image":     {captureAnswer{logBin: true, format: "ROW", image: "MINIMAL"}, "binlog_row_image = FULL (it is MINIMAL)"},
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

func TestTheMySQLDriverOpensNoChangeStreamYet(t *testing.T) {
	_, err := (&mysqlDriver{}).Capture(context.Background(), "", capture.Request{})
	if !errors.Is(err, capture.ErrUnsupported) || !strings.Contains(err.Error(), "binary log") {
		t.Errorf("Capture returned %v", err)
	}
}
