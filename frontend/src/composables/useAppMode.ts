import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { modeOfPath, type AppMode } from '../lib/mode'

/** 当前所在的项目（RelayMesh / ExecMesh）：由地址栏的路径前缀决定。 */
export function useAppMode() {
  const route = useRoute()
  return computed<AppMode>(() => modeOfPath(route.path))
}
