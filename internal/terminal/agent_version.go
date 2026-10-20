package terminal

import "github.com/teacat99/RelayMesh/v2/internal/termproto"

// Agent 版本的比较（「这台主机的 Agent 比服务端自带的旧」这类提醒）与 Agent 自己拒绝「升级到不比自己新的版本」用的是同一份判断，
// 实现放在协议包里（termproto/agentversion.go）；这里只保留服务端习惯的名字。

// versionRelation 描述主机当前的 Agent 版本相对服务端自带版本的关系。
type versionRelation = termproto.VersionRelation

const (
	// versionCurrent：一致，或主机的版本更新（例如服务端回退过）——都不需要提醒。
	versionCurrent = termproto.VersionCurrent
	// versionOlder：能确定主机的版本更旧。
	versionOlder = termproto.VersionOlder
	// versionDiffers：与服务端自带的版本不同，但无法比较新旧（短哈希之类）。
	versionDiffers = termproto.VersionDiffers
)

// relateAgentVersion 比较主机当前版本 cur 与服务端自带的版本 latest。任何一边为空都按「无需提醒」处理。
func relateAgentVersion(cur, latest string) versionRelation {
	return termproto.RelateAgentVersion(cur, latest)
}

// 升级提醒的种类（HostView.UpgradeKind）。
const (
	// older：能从版本号确定主机更旧（指纹存在时同时确认了 Agent 代码确实变了）。
	upgradeKindOlder = "older"
	// different：版本号不同但无法比较新旧，且至少有一边没有指纹（回退到比版本号）。
	upgradeKindDiffers = "different"
	// changed：两边都有指纹且不同——Agent 代码确实变了，但版本号无法比较新旧（例如同一天的两次构建）。
	upgradeKindChanged = "changed"
)

// upgradeVerdict 是「这台主机该不该升级」的判断结果。
type upgradeVerdict struct {
	// Available 为 true 时有可升级的新版本；Kind 是 upgradeKind* 之一。
	Available bool
	Kind      string
	// SameCode 为 true 表示两边都有指纹且一致、但版本号不同：这只是构建号不同，Agent 代码完全一致，不需要升级。
	SameCode bool
}

// judgeUpgrade 判断主机（curVer / curRev 是它自报的版本与代码指纹）相对服务端自带的这批 Agent（latestVer / latestRev）要不要升级。
//
// 版本号跟着整个仓库的构建走：服务端每热更一次就变，Agent 依赖的代码没变也会变。所以两边都有指纹时只比指纹——
//   - 指纹一致：不提醒（版本号不同时标 SameCode，界面据此说明「代码一致」）；
//   - 指纹不同：Agent 代码确实变了。版本号能确定主机更旧 → older；无法比较新旧 → changed；
//     版本号一致或主机更新（服务端回退过、同版本号的本地构建）→ 不提醒，不拿服务端较旧的一批去「升级」主机。
//
// 任何一边没有指纹（旧版本 Agent、没有 REV 文件的部署、非发布构建）时回退到只比版本号，与引入指纹之前的行为一致。
func judgeUpgrade(curVer, curRev, latestVer, latestRev string) upgradeVerdict {
	rel := relateAgentVersion(curVer, latestVer)
	if same, known := termproto.CompareAgentRev(curRev, latestRev); known {
		if same {
			return upgradeVerdict{SameCode: curVer != "" && latestVer != "" && curVer != latestVer}
		}
		switch rel {
		case versionOlder:
			return upgradeVerdict{Available: true, Kind: upgradeKindOlder}
		case versionDiffers:
			return upgradeVerdict{Available: true, Kind: upgradeKindChanged}
		}
		return upgradeVerdict{}
	}
	switch rel {
	case versionOlder:
		return upgradeVerdict{Available: true, Kind: upgradeKindOlder}
	case versionDiffers:
		return upgradeVerdict{Available: true, Kind: upgradeKindDiffers}
	}
	return upgradeVerdict{}
}
