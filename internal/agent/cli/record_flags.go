package cli

import (
	"flag"
	"fmt"
	"strconv"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
	"github.com/teacat99/RelayMesh/v2/internal/agent/record"
)

const envNoRecord = "RELAYMESH_AGENT_NO_RECORD"

// recordFlags 是终端记录（终端上显示什么就记录什么，只写在本机）的选项。
// 服务端只能让它更严格（关闭录制 / 缩短保留 / 降低配额），不能放宽这里的任何一项。
type recordFlags struct {
	noRecord      bool
	sessionMB     int
	sessionLines  int
	hostMB        int
	retentionDays int
	minFreeMB     int
}

func (rf *recordFlags) bind(fs *flag.FlagSet, env Env) {
	fs.BoolVar(&rf.noRecord, "no-record", truthy(env.Getenv(envNoRecord)),
		"本机一票否决：不录制任何终端会话（服务端无法覆盖；已有记录仍可读取，直到过期）")
	fs.IntVar(&rf.sessionMB, "record-session-mb", 0, "单个会话保留的记录上限，MiB（默认 64；超出后滚动丢弃最旧部分）")
	fs.IntVar(&rf.sessionLines, "record-session-lines", 0, "单个会话保留的输出行数上限（按换行符计，默认不限制；超出后滚动丢弃最旧部分，与字节上限互相独立）")
	fs.IntVar(&rf.hostMB, "record-host-mb", 0, "本机全部记录的磁盘占用上限，MiB（默认 512）")
	fs.IntVar(&rf.retentionDays, "record-retention-days", 0, "会话结束后记录的保留天数（默认 30）")
	fs.IntVar(&rf.minFreeMB, "record-min-free-mb", 0, "磁盘剩余空间低于该值时停止录制，MiB（默认 1024；-1 关闭这项保护）")
}

func (rf *recordFlags) validate() error {
	for name, v := range map[string]int{
		"--record-session-mb": rf.sessionMB, "--record-session-lines": rf.sessionLines, "--record-host-mb": rf.hostMB, "--record-retention-days": rf.retentionDays,
	} {
		if v < 0 {
			return fmt.Errorf("%s 不能为负数", name)
		}
	}
	if rf.minFreeMB < -1 {
		return fmt.Errorf("--record-min-free-mb 只能是 -1（关闭保护）或非负数")
	}
	return nil
}

// options 把选项折算成记录存储的配置；零值字段交给存储取默认值。
func (rf *recordFlags) options(dataDir string) record.Options {
	o := record.Options{
		Dir:          holder.RecordDir(dataDir),
		Disabled:     rf.noRecord,
		SessionBytes: int64(rf.sessionMB) << 20,
		SessionLines: int64(rf.sessionLines),
		HostBytes:    int64(rf.hostMB) << 20,
		Harden:       agent.HardenDir,
	}
	if rf.retentionDays > 0 {
		o.Retention = time.Duration(rf.retentionDays) * 24 * time.Hour
	}
	switch {
	case rf.minFreeMB < 0:
		o.MinFree = -1
	case rf.minFreeMB > 0:
		o.MinFree = int64(rf.minFreeMB) << 20
	}
	return o
}

// args 还原成服务单元里的参数（只写用户显式给出的值）。
func (rf *recordFlags) args() []string {
	var a []string
	if rf.noRecord {
		a = append(a, "--no-record")
	}
	if rf.sessionMB > 0 {
		a = append(a, "--record-session-mb", strconv.Itoa(rf.sessionMB))
	}
	if rf.sessionLines > 0 {
		a = append(a, "--record-session-lines", strconv.Itoa(rf.sessionLines))
	}
	if rf.hostMB > 0 {
		a = append(a, "--record-host-mb", strconv.Itoa(rf.hostMB))
	}
	if rf.retentionDays > 0 {
		a = append(a, "--record-retention-days", strconv.Itoa(rf.retentionDays))
	}
	if rf.minFreeMB != 0 {
		a = append(a, "--record-min-free-mb", strconv.Itoa(rf.minFreeMB))
	}
	return a
}
