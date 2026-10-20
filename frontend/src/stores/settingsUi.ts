import { defineStore } from 'pinia'
import { reactive, ref } from 'vue'
import type { AppMode } from '../lib/mode'
import { categoryById, firstCategory, resolveCategory, type SettingsCategoryId } from '../lib/settingsRegistry'

/**
 * 设置窗口当前在看哪个模块的哪个分类。
 *
 * 设置窗口是「当前模块的设置」：窗口里只有当前模块自己的分类和通用分类，另一个模块的分类要通过
 * 窗口里的「RelayMesh | ExecMesh」分段切换进入（背景页面同时切到那个模块）。
 * 这里只记界面状态；设置的内容仍在 settings 仓库与各自的接口里。
 */
export const useSettingsUiStore = defineStore('settingsUi', () => {
  const module = ref<AppMode>('relay')
  const category = ref<SettingsCategoryId>(firstCategory('relay'))
  /** 每个模块上次停留的专属分类：切过去再切回来时回到原来的位置。 */
  const lastOwn = reactive<Record<AppMode, SettingsCategoryId>>({
    relay: firstCategory('relay'),
    exec: firstCategory('exec'),
  })

  /** 选中某个分类（必须是当前模块可见的，否则回到当前模块的第一个分类）。 */
  function select(id: string) {
    category.value = resolveCategory(module.value, id)
    const c = categoryById(category.value)
    if (c && c.owner !== 'common') lastOwn[c.owner] = category.value
  }

  /**
   * 进入某个模块的设置。
   * - 指定了分类：落到那个分类（不属于该模块也不是通用的则回到第一个）；
   * - 没指定且模块没变：保持当前分类（重新打开窗口时停在原来的位置）；
   * - 没指定且模块变了：回到该模块上次停留的分类。
   */
  function enter(next: AppMode, wanted?: string) {
    const sameModule = next === module.value
    module.value = next
    select(wanted ?? (sameModule ? category.value : lastOwn[next]))
  }

  return { module, category, select, enter }
})
