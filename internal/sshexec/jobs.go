package sshexec

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 远端「后台任务」协议。
//
// 每次模型运行在目标主机上对应一个任务目录 ~/.relaymesh/runs/<run_id>（权限 700）：
//
//	cmd.sh      命令文本（经 base64 由 stdin 传输，绝不拼进命令行）
//	cwd / shell / timeout   启动参数
//	run.sh      运行器：写 pid、切换目录、执行命令、写 exit_code 与 finished
//	pid         运行器的进程号（用 setsid 启动时同时是进程组号）
//	stdout.log / stderr.log   两路输出（完整输出只保存在这里，Hub 数据库不保存）
//	exit_code   命令的退出码（运行器写完才出现，写入是原子的）
//
// 所有脚本都通过 `sh -s` 从 stdin 读入，参数只来自本文件里经过严格校验的值：
// 运行 ID 必须是 32 位小写十六进制、数字用 strconv 格式化、命令与目录先 base64、shell 来自白名单。
// 脚本全部幂等：连接中途断开后用新连接重放不会造成重复执行。

// RemoteShell 是执行脚本时使用的命令（脚本从 stdin 输入）。
const RemoteShell = "sh -s"

// MaxChunkBytes 是一次轮询在每一路上最多取回的原始字节数。
const MaxChunkBytes = 256 << 10

var runIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidRunID 判断运行 ID 是否可以安全地出现在远端脚本里。
func ValidRunID(id string) bool { return runIDRe.MatchString(id) }

var allowedRemoteShells = map[string]struct{}{"": {}, "bash": {}, "sh": {}, "zsh": {}}

// ValidRemoteShell 判断 shell 名称是否允许用于 SSH 主机。
func ValidRemoteShell(s string) bool {
	_, ok := allowedRemoteShells[s]
	return ok
}

func fill(tpl string, kv ...string) string {
	return strings.NewReplacer(kv...).Replace(tpl)
}

func wrap76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76])
		b.WriteByte('\n')
		s = s[76:]
	}
	b.WriteString(s)
	return b.String()
}

const scriptPrelude = `LC_ALL=C; export LC_ALL
[ -n "${HOME:-}" ] || { echo "RM_ERR home"; exit 0; }
`

// ---------- 能力探测 ----------

const factsScript = `LC_ALL=C; export LC_ALL
echo "RM_OS $(uname -s 2>/dev/null)"
echo "RM_ARCH $(uname -m 2>/dev/null)"
echo "RM_HOST $(hostname 2>/dev/null || uname -n 2>/dev/null)"
echo "RM_HOME ${HOME:-}"
echo "RM_USER $(id -un 2>/dev/null)"
if command -v setsid >/dev/null 2>&1; then echo "RM_SETSID 1"; else echo "RM_SETSID 0"; fi
if command -v timeout >/dev/null 2>&1; then echo "RM_TIMEOUT 1"; else echo "RM_TIMEOUT 0"; fi
if command -v bash >/dev/null 2>&1; then echo "RM_BASH 1"; else echo "RM_BASH 0"; fi
if echo QQ== | base64 -d >/dev/null 2>&1 || echo QQ== | base64 -D >/dev/null 2>&1; then echo "RM_B64 1"; else echo "RM_B64 0"; fi
`

// Facts 是目标主机的环境信息。
type Facts struct {
	OS, Arch, Host, Home, User string
	Setsid, Timeout, Bash, B64 bool
}

// FactsCommand 返回探测脚本。
func FactsCommand() (cmd string, stdin []byte) { return RemoteShell, []byte(factsScript) }

// ParseFacts 解析探测输出；不是 POSIX 环境（没有 uname）或缺少 base64 时返回 ErrUnsupported。
func ParseFacts(out []byte) (Facts, error) {
	var f Facts
	seenOS := false
	for _, line := range rmLines(out) {
		key, val := splitRM(line)
		switch key {
		case "OS":
			f.OS, seenOS = val, val != ""
		case "ARCH":
			f.Arch = val
		case "HOST":
			f.Host = val
		case "HOME":
			f.Home = val
		case "USER":
			f.User = val
		case "SETSID":
			f.Setsid = val == "1"
		case "TIMEOUT":
			f.Timeout = val == "1"
		case "BASH":
			f.Bash = val == "1"
		case "B64":
			f.B64 = val == "1"
		}
	}
	if !seenOS || f.Home == "" {
		return f, ErrUnsupported
	}
	if !f.B64 {
		return f, fmt.Errorf("%w：缺少 base64 命令", ErrUnsupported)
	}
	return f, nil
}

// ---------- 启动 ----------

const launchScript = scriptPrelude + `BASE="$HOME/.relaymesh/runs"
JOB="$BASE/@RUN@"
if [ -s "$JOB/pid" ]; then
  echo "RM_DUP 1"
  echo "RM_PID $(cat "$JOB/pid" 2>/dev/null)"
  echo "RM_SHELL $(cat "$JOB/shell_used" 2>/dev/null)"
  exit 0
fi
umask 077
mkdir -p "$BASE" "$JOB" 2>/dev/null || { echo "RM_ERR mkdir"; exit 0; }
chmod 700 "$HOME/.relaymesh" "$BASE" "$JOB" 2>/dev/null
if echo QQ== | base64 -d >/dev/null 2>&1; then B64D="base64 -d"; elif echo QQ== | base64 -D >/dev/null 2>&1; then B64D="base64 -D"; else echo "RM_ERR b64"; exit 0; fi
$B64D > "$JOB/cmd.sh" <<'RM_B64_EOF' || { echo "RM_ERR decode"; exit 0; }
@CMD@
RM_B64_EOF
$B64D > "$JOB/cwd" <<'RM_B64_EOF' || { echo "RM_ERR decode"; exit 0; }
@CWD@
RM_B64_EOF
printf '%s' '@SHELL@' > "$JOB/shell"
printf '%s' '@TIMEOUT@' > "$JOB/timeout"
CWD=$(cat "$JOB/cwd" 2>/dev/null)
if [ -n "$CWD" ] && [ ! -d "$CWD" ]; then rm -rf "$JOB"; echo "RM_ERR cwd"; exit 0; fi
cat > "$JOB/run.sh" <<'RM_RUNNER_EOF'
JOB="$1"
cd "$JOB" || exit 1
SH=$(cat shell)
if [ -z "$SH" ]; then if command -v bash >/dev/null 2>&1; then SH=bash; else SH=sh; fi; fi
echo "$SH" > shell_used
if [ "$(ps -o sid= -p $$ 2>/dev/null | tr -d ' ')" = "$$" ]; then echo session > mode; else echo tree > mode; fi
echo $$ > pid.tmp && mv pid.tmp pid
CWD=$(cat cwd)
TSEC=$(cat timeout)
# 没有指定工作目录时在家目录里运行（与 Agent 主机一致）；任务目录会被定期清理，不能留在里面。
if [ -z "$CWD" ]; then CWD="$HOME"; fi
if ! cd "$CWD" 2>/dev/null; then
  echo "cd: $CWD: 无法进入工作目录" > "$JOB/stderr.log"
  : > "$JOB/stdout.log"
  echo 126 > exit_code.tmp && mv exit_code.tmp exit_code
  date +%s > finished
  exit 0
fi
START=$(date +%s)
if command -v timeout >/dev/null 2>&1 && [ "${TSEC:-0}" -gt 0 ] 2>/dev/null; then
  timeout "$TSEC" "$SH" "$JOB/cmd.sh" > "$JOB/stdout.log" 2> "$JOB/stderr.log" < /dev/null
else
  "$SH" "$JOB/cmd.sh" > "$JOB/stdout.log" 2> "$JOB/stderr.log" < /dev/null
fi
EC=$?
# GNU timeout 超时返回 124；busybox 等实现返回被终止的子进程的状态（128 加信号，TERM 为 143）。
# 统一约定：被信号结束、且已运行满超时时长，就按超时（124）处理——否则这类主机上的超时会被误报成「被 SIGTERM 终止」。
if [ "$EC" -gt 128 ] && [ "${TSEC:-0}" -gt 0 ] 2>/dev/null; then
  if [ $(( $(date +%s) - START )) -ge "$TSEC" ]; then EC=124; fi
fi
echo "$EC" > "$JOB/exit_code.tmp" && mv "$JOB/exit_code.tmp" "$JOB/exit_code"
date +%s > "$JOB/finished"
RM_RUNNER_EOF
chmod 700 "$JOB/run.sh" "$JOB/cmd.sh"
if command -v setsid >/dev/null 2>&1; then
  setsid sh "$JOB/run.sh" "$JOB" </dev/null >/dev/null 2>&1 &
else
  nohup sh "$JOB/run.sh" "$JOB" </dev/null >/dev/null 2>&1 &
fi
i=0
while [ ! -s "$JOB/pid" ] && [ "$i" -lt 60 ]; do
  sleep 0.1 2>/dev/null || sleep 1
  i=$((i+1))
done
if [ ! -s "$JOB/pid" ]; then echo "RM_ERR spawn"; exit 0; fi
echo "RM_PID $(cat "$JOB/pid" 2>/dev/null)"
echo "RM_SHELL $(cat "$JOB/shell_used" 2>/dev/null)"
exit 0
`

// LaunchSpec 是一次启动的参数。
type LaunchSpec struct {
	RunID      string
	Command    string
	Shell      string // 空表示自动选择（bash，其次 sh）
	Cwd        string // 空表示家目录
	TimeoutSec int
}

// LaunchCommand 生成启动脚本。
func LaunchCommand(sp LaunchSpec) (cmd string, stdin []byte, err error) {
	switch {
	case !ValidRunID(sp.RunID):
		return "", nil, fmt.Errorf("%w：运行 ID 无效", ErrProtocol)
	case !ValidRemoteShell(sp.Shell):
		return "", nil, fmt.Errorf("%w：shell 无效", ErrProtocol)
	case sp.TimeoutSec < 0 || sp.TimeoutSec > 7*24*3600:
		return "", nil, fmt.Errorf("%w：超时无效", ErrProtocol)
	}
	script := fill(launchScript,
		"@RUN@", sp.RunID,
		"@CMD@", wrap76(base64.StdEncoding.EncodeToString([]byte(sp.Command))),
		"@CWD@", wrap76(base64.StdEncoding.EncodeToString([]byte(sp.Cwd))),
		"@SHELL@", sp.Shell,
		"@TIMEOUT@", strconv.Itoa(sp.TimeoutSec),
	)
	return RemoteShell, []byte(script), nil
}

// 启动失败的原因（LaunchResult.Err）。
const (
	LaunchErrHome   = "home"
	LaunchErrMkdir  = "mkdir"
	LaunchErrB64    = "b64"
	LaunchErrDecode = "decode"
	LaunchErrCwd    = "cwd"
	LaunchErrSpawn  = "spawn"
)

// LaunchResult 是启动脚本的输出。
type LaunchResult struct {
	PID       int
	Shell     string
	Duplicate bool   // 任务目录已存在：脚本被重放，没有再次启动
	Err       string // 非空表示没有启动
}

// ParseLaunch 解析启动输出。
func ParseLaunch(out []byte) (LaunchResult, error) {
	var r LaunchResult
	for _, line := range rmLines(out) {
		key, val := splitRM(line)
		switch key {
		case "ERR":
			r.Err = val
		case "PID":
			r.PID, _ = strconv.Atoi(val)
		case "SHELL":
			r.Shell = val
		case "DUP":
			r.Duplicate = true
		}
	}
	if r.Err == "" && r.PID <= 0 {
		return r, fmt.Errorf("%w：缺少进程号", ErrProtocol)
	}
	return r, nil
}

// ---------- 轮询 / 读取 ----------

const chunkFuncs = `size() { if [ -f "$1" ]; then n=$(wc -c < "$1" 2>/dev/null); echo $((n+0)); else echo 0; fi; }
chunk() {
  [ "$3" -lt "$2" ] 2>/dev/null || return 0
  echo "RM_DATA_$1"
  tail -c +$(($3+1)) "$4" | head -c "$5" | base64 | tr -d '\n'
  echo
  echo "RM_END"
}
`

const pollScript = scriptPrelude + `JOB="$HOME/.relaymesh/runs/@RUN@"
[ -d "$JOB" ] || { echo "RM_GONE"; exit 0; }
PID=""; [ -f "$JOB/pid" ] && PID=$(cat "$JOB/pid" 2>/dev/null)
EC=""; [ -f "$JOB/exit_code" ] && EC=$(cat "$JOB/exit_code" 2>/dev/null)
ALIVE=0
if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then ALIVE=1; fi
if [ -z "$EC" ] && [ "$ALIVE" = 0 ] && [ -f "$JOB/exit_code" ]; then EC=$(cat "$JOB/exit_code" 2>/dev/null); fi
FIN=""; [ -f "$JOB/finished" ] && FIN=$(cat "$JOB/finished" 2>/dev/null)
` + chunkFuncs + `OS=$(size "$JOB/stdout.log"); ES=$(size "$JOB/stderr.log")
echo "RM_ST pid=${PID:-0} alive=$ALIVE ec=${EC:--} fin=${FIN:--} out=$OS err=$ES"
chunk OUT "$OS" @OOFF@ "$JOB/stdout.log" @MAX@
chunk ERR "$ES" @EOFF@ "$JOB/stderr.log" @MAX@
`

// PollSpec 是一次轮询的参数：从两路输出的指定偏移起取回新增内容。
type PollSpec struct {
	RunID    string
	OutOff   int64
	ErrOff   int64
	MaxBytes int // 每一路最多取回的字节数，缺省并限制为 MaxChunkBytes
}

// PollCommand 生成轮询脚本。
func PollCommand(sp PollSpec) (cmd string, stdin []byte, err error) {
	if !ValidRunID(sp.RunID) {
		return "", nil, fmt.Errorf("%w：运行 ID 无效", ErrProtocol)
	}
	if sp.OutOff < 0 || sp.ErrOff < 0 {
		return "", nil, fmt.Errorf("%w：偏移无效", ErrProtocol)
	}
	max := sp.MaxBytes
	if max <= 0 || max > MaxChunkBytes {
		max = MaxChunkBytes
	}
	script := fill(pollScript,
		"@RUN@", sp.RunID,
		"@OOFF@", strconv.FormatInt(sp.OutOff, 10),
		"@EOFF@", strconv.FormatInt(sp.ErrOff, 10),
		"@MAX@", strconv.Itoa(max),
	)
	return RemoteShell, []byte(script), nil
}

// PollResult 是轮询的结果。
type PollResult struct {
	Gone     bool   // 任务目录不存在（已被清理或从未创建）
	Problem  string // 脚本自身报告的问题（例如 home）
	PID      int
	Alive    bool
	ExitCode *int  // 运行器写出了退出码才有值
	Finished int64 // 结束时间（Unix 秒，主机时钟），未结束为 0
	OutSize  int64
	ErrSize  int64
	Out, Err []byte // 从请求的偏移起取回的新增内容
}

// section 读取 RM_DATA_* 标记之后的数据段：一行 base64，随后是 RM_END。返回解码后的数据与下一个待处理的行号。
func section(lines []string, i int) ([]byte, int, error) {
	j := i + 1
	if j >= len(lines) {
		return nil, j, fmt.Errorf("%w：数据段被截断", ErrProtocol)
	}
	if lines[j] == "RM_END" {
		return nil, j, nil
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[j]))
	if err != nil {
		return nil, j, fmt.Errorf("%w：数据段不是合法的 base64", ErrProtocol)
	}
	if j+1 >= len(lines) || lines[j+1] != "RM_END" {
		return nil, j, fmt.Errorf("%w：数据段缺少结束标记", ErrProtocol)
	}
	return data, j + 1, nil
}

// ParsePoll 解析轮询输出。
func ParsePoll(out []byte) (PollResult, error) {
	var r PollResult
	seen := false
	lines := splitLines(out)
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "RM_") {
			continue
		}
		key, val := splitRM(lines[i])
		switch key {
		case "GONE":
			r.Gone, seen = true, true
		case "ERR":
			r.Problem, seen = val, true
		case "ST":
			seen = true
			for _, kv := range strings.Fields(val) {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					continue
				}
				switch k {
				case "pid":
					r.PID, _ = strconv.Atoi(v)
				case "alive":
					r.Alive = v == "1"
				case "ec":
					if v != "-" {
						n, err := strconv.Atoi(v)
						if err != nil || n < 0 || n > 255 {
							return r, fmt.Errorf("%w：退出码无效", ErrProtocol)
						}
						r.ExitCode = &n
					}
				case "fin":
					if v != "-" {
						r.Finished, _ = strconv.ParseInt(v, 10, 64)
					}
				case "out":
					r.OutSize, _ = strconv.ParseInt(v, 10, 64)
				case "err":
					r.ErrSize, _ = strconv.ParseInt(v, 10, 64)
				}
			}
		case "DATA_OUT", "DATA_ERR":
			data, next, err := section(lines, i)
			if err != nil {
				return r, err
			}
			if key == "DATA_OUT" {
				r.Out = data
			} else {
				r.Err = data
			}
			i = next
		}
	}
	if !seen {
		return r, fmt.Errorf("%w：缺少状态行", ErrProtocol)
	}
	return r, nil
}

const readScript = scriptPrelude + `JOB="$HOME/.relaymesh/runs/@RUN@"
[ -d "$JOB" ] || { echo "RM_GONE"; exit 0; }
` + chunkFuncs + `F="$JOB/@FILE@"
SZ=$(size "$F")
echo "RM_SIZE $SZ"
chunk OUT "$SZ" @OFF@ "$F" @MAX@
`

// ReadSpec 是读取某一路输出的一页的参数。
type ReadSpec struct {
	RunID  string
	Stream string // stdout | stderr
	Off    int64
	Max    int
}

// ReadCommand 生成读取脚本。
func ReadCommand(sp ReadSpec) (cmd string, stdin []byte, err error) {
	if !ValidRunID(sp.RunID) {
		return "", nil, fmt.Errorf("%w：运行 ID 无效", ErrProtocol)
	}
	file := ""
	switch sp.Stream {
	case "stdout":
		file = "stdout.log"
	case "stderr":
		file = "stderr.log"
	default:
		return "", nil, fmt.Errorf("%w：流无效", ErrProtocol)
	}
	if sp.Off < 0 {
		return "", nil, fmt.Errorf("%w：偏移无效", ErrProtocol)
	}
	max := sp.Max
	if max <= 0 || max > MaxChunkBytes {
		max = MaxChunkBytes
	}
	script := fill(readScript,
		"@RUN@", sp.RunID, "@FILE@", file,
		"@OFF@", strconv.FormatInt(sp.Off, 10), "@MAX@", strconv.Itoa(max),
	)
	return RemoteShell, []byte(script), nil
}

// ReadResult 是读取的结果。
type ReadResult struct {
	Gone    bool
	Problem string
	Size    int64
	Data    []byte
}

// ParseRead 解析读取输出。
func ParseRead(out []byte) (ReadResult, error) {
	var r ReadResult
	seen := false
	lines := splitLines(out)
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "RM_") {
			continue
		}
		key, val := splitRM(lines[i])
		switch key {
		case "GONE":
			r.Gone, seen = true, true
		case "ERR":
			r.Problem, seen = val, true
		case "SIZE":
			r.Size, _ = strconv.ParseInt(val, 10, 64)
			seen = true
		case "DATA_OUT":
			data, next, err := section(lines, i)
			if err != nil {
				return r, err
			}
			r.Data = data
			i = next
		}
	}
	if !seen {
		return r, fmt.Errorf("%w：缺少状态行", ErrProtocol)
	}
	return r, nil
}

// ---------- 取消 ----------

const cancelScript = scriptPrelude + `JOB="$HOME/.relaymesh/runs/@RUN@"
[ -d "$JOB" ] || { echo "RM_GONE"; exit 0; }
if [ -f "$JOB/exit_code" ]; then echo "RM_DONE"; exit 0; fi
PID=$(cat "$JOB/pid" 2>/dev/null)
[ -n "$PID" ] || { echo "RM_NOPID"; exit 0; }
MODE=$(cat "$JOB/mode" 2>/dev/null)
kids() { ps -A -o pid= -o ppid= 2>/dev/null | while read -r p pp; do [ "$pp" = "$1" ] && echo "$p"; done; }
tree() { if kill -0 "$1" 2>/dev/null; then echo "$1"; fi; for c in $(kids "$1"); do tree "$c"; done; }
members() { ps -A -o pid= -o sid= -o stat= 2>/dev/null | while read -r p s st; do case "$st" in Z*) continue ;; esac; [ "$s" = "$PID" ] && echo "$p"; done; }
collect() { if [ "$MODE" = session ]; then members; else tree "$PID"; fi; }
sig() { for p in $(collect); do kill -s "$1" "$p" 2>/dev/null; done; }
sig TERM
i=0
while [ "$i" -lt 30 ]; do
  [ -z "$(collect)" ] && break
  sleep 0.1 2>/dev/null || sleep 1
  i=$((i+1))
done
if [ -n "$(collect)" ]; then sig KILL; sleep 0.3 2>/dev/null; echo "RM_KILLED"; else echo "RM_TERMED"; fi
`

// CancelCommand 生成取消脚本：对整个进程组先发 TERM，宽限约 3 秒后发 KILL。
func CancelCommand(runID string) (cmd string, stdin []byte, err error) {
	if !ValidRunID(runID) {
		return "", nil, fmt.Errorf("%w：运行 ID 无效", ErrProtocol)
	}
	return RemoteShell, []byte(fill(cancelScript, "@RUN@", runID)), nil
}

// CancelResult 是取消脚本的输出。
type CancelResult struct {
	Gone   bool // 任务目录不存在
	Done   bool // 命令已经自行结束
	NoPID  bool
	Killed bool // 宽限期后仍存活，被 KILL
	Termed bool // TERM 后退出
}

// ParseCancel 解析取消输出。
func ParseCancel(out []byte) (CancelResult, error) {
	var r CancelResult
	seen := false
	for _, line := range rmLines(out) {
		key, _ := splitRM(line)
		switch key {
		case "GONE":
			r.Gone, seen = true, true
		case "DONE":
			r.Done, seen = true, true
		case "NOPID":
			r.NoPID, seen = true, true
		case "KILLED":
			r.Killed, seen = true, true
		case "TERMED":
			r.Termed, seen = true, true
		case "ERR":
			seen = true
		}
	}
	if !seen {
		return r, fmt.Errorf("%w：取消没有返回结果", ErrProtocol)
	}
	return r, nil
}

// ---------- 清理 ----------

const sweepScript = scriptPrelude + `BASE="$HOME/.relaymesh/runs"
[ -d "$BASE" ] || { echo "RM_SWEPT 0"; exit 0; }
N=$(find "$BASE" -mindepth 1 -maxdepth 1 -type d -mtime +@DAYS@ 2>/dev/null | while IFS= read -r d; do
  case "$d" in "$BASE"/????????????????????????????????) ;; *) continue ;; esac
  if [ ! -f "$d/exit_code" ] && [ -s "$d/pid" ] && kill -0 "$(cat "$d/pid" 2>/dev/null)" 2>/dev/null; then continue; fi
  rm -rf "$d" && echo x
done | wc -l)
echo "RM_SWEPT $((N+0))"
`

// SweepCommand 生成清理脚本：删除已结束且超过 days 天的任务目录（仍在运行的不动）。
func SweepCommand(days int) (cmd string, stdin []byte, err error) {
	if days < 1 || days > 3650 {
		return "", nil, fmt.Errorf("%w：保留天数无效", ErrProtocol)
	}
	return RemoteShell, []byte(fill(sweepScript, "@DAYS@", strconv.Itoa(days))), nil
}

// ---------- 解析工具 ----------

func splitLines(out []byte) []string {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		lines = append(lines, strings.TrimRight(sc.Text(), "\r"))
	}
	return lines
}

// rmLines 只保留协议行（RM_ 开头）：登录脚本可能在输出前后混入别的内容。
func rmLines(out []byte) []string {
	var keep []string
	for _, l := range splitLines(out) {
		if strings.HasPrefix(l, "RM_") {
			keep = append(keep, l)
		}
	}
	return keep
}

func splitRM(line string) (key, val string) {
	line = strings.TrimPrefix(line, "RM_")
	key, val, _ = strings.Cut(line, " ")
	return key, strings.TrimSpace(val)
}
