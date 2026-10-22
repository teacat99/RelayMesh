package holder

import "github.com/teacat99/RelayMesh/v2/internal/termstate"

// 环形缓冲与模式跟踪器已抽到 internal/termstate，与 Hub 本地会话共用；这里保留原名，会话代码无需改动。

// Ring 是按累计字节偏移寻址的环形缓冲（见 termstate.Ring）。
type Ring = termstate.Ring

// ModeTracker 跟踪重新附着时必须恢复的终端模式（见 termstate.ModeTracker）。
type ModeTracker = termstate.ModeTracker

// ErrOverwritten 表示请求的偏移已经被环形缓冲覆盖。
var ErrOverwritten = termstate.ErrOverwritten

// NewRing 创建容量为 size 字节的环形缓冲。
func NewRing(size int) *Ring { return termstate.NewRing(size) }

// NewModeTracker 创建处于初始状态的模式跟踪器。
func NewModeTracker() *ModeTracker { return termstate.NewModeTracker() }
