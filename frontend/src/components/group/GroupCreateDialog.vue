<script setup lang="ts">
// 新建群聊：起个名字，勾选要拉进群的 Agent（必须处于聊天模式；不满足的置灰并说明原因）。
import { computed, nextTick, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { chatErrorMessage } from '../../api/conversations'
import { settleDialogHistory } from '../../composables/useDialogBackGesture'
import { useGroupStore } from '../../stores/groups'
import Dialog from '../ui/dialog/Dialog.vue'
import DialogContent from '../ui/dialog/DialogContent.vue'
import DialogHeader from '../ui/dialog/DialogHeader.vue'
import DialogTitle from '../ui/dialog/DialogTitle.vue'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ (e: 'update:open', v: boolean): void; (e: 'created', id: string): void }>()

const groups = useGroupStore()
const title = ref('')
const picked = ref<string[]>([])
const saving = ref(false)

const canCreate = computed(() => !saving.value && title.value.trim().length > 0 && picked.value.length > 0)

watch(
  () => props.open,
  async open => {
    if (!open) return
    title.value = ''
    picked.value = []
    try {
      await groups.loadCandidates()
    } catch (e) {
      toast.error(chatErrorMessage(e, '候选成员加载失败'))
    }
  },
  { immediate: true },
)

async function create() {
  if (!canCreate.value) return
  saving.value = true
  try {
    const id = await groups.create(title.value.trim(), picked.value)
    emit('update:open', false)
    // 弹窗打开时压了一条历史哨兵，关闭时 history.back() 异步弹掉它：不等它走完就跳转，随后到来的回退会把刚进入的群又退回列表。
    await nextTick()
    await settleDialogHistory()
    emit('created', id)
  } catch (e) {
    toast.error(chatErrorMessage(e, '创建失败'))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog :open="props.open" @update:open="v => emit('update:open', v)">
    <DialogContent :show-close="true" class="w-[94vw] gap-3 sm:max-w-md" data-testid="group-create-dialog">
      <DialogHeader>
        <DialogTitle class="text-sm font-semibold">新建群聊</DialogTitle>
      </DialogHeader>

      <input
        v-model="title"
        maxlength="60"
        placeholder="群名，例如「接口联调」"
        class="h-9 rounded-md border border-input bg-background px-2.5 text-[13px] outline-none focus-visible:ring-1 focus-visible:ring-ring"
        data-testid="group-create-title"
        @keydown.enter="create"
      />

      <div class="text-[12px] text-muted-foreground">选择成员（只有开启了聊天模式的 Agent 可以加入）</div>
      <ul class="max-h-64 space-y-0.5 overflow-y-auto" data-testid="group-create-candidates">
        <li v-if="groups.candidatesLoading" class="px-1 py-2 text-[12px] text-muted-foreground">加载中…</li>
        <li v-else-if="!groups.candidates.length" class="px-1 py-2 text-[12px] text-muted-foreground">还没有 Agent 工作流。</li>
        <li v-for="c in groups.candidates" :key="c.workflow_id">
          <label
            class="flex items-start gap-2 rounded px-1.5 py-1.5 text-[13px]"
            :class="c.eligible ? 'cursor-pointer hover:bg-accent/60' : 'opacity-55'"
            :data-eligible="c.eligible"
          >
            <input v-model="picked" type="checkbox" class="mt-0.5 size-3.5" :value="c.workflow_id" :disabled="!c.eligible" :data-testid="`group-candidate-${c.workflow_id}`" />
            <span class="min-w-0 flex-1">
              <span class="block truncate">{{ c.name || c.workflow_id }}</span>
              <span class="block truncate font-mono text-[10px] text-muted-foreground">{{ c.workflow_id }}</span>
              <span v-if="!c.eligible" class="block text-[11px] text-muted-foreground">{{ c.reason }}</span>
            </span>
          </label>
        </li>
      </ul>

      <div class="flex justify-end gap-2">
        <button type="button" class="h-8 cursor-pointer rounded-md border border-border px-3 text-[12px] hover:bg-accent" @click="emit('update:open', false)">取消</button>
        <button
          type="button"
          class="h-8 cursor-pointer rounded-md bg-primary px-3 text-[12px] text-primary-foreground disabled:cursor-not-allowed disabled:opacity-40"
          :disabled="!canCreate"
          data-testid="group-create-submit"
          @click="create"
        >
          创建{{ picked.length ? `（${picked.length} 位成员）` : '' }}
        </button>
      </div>
    </DialogContent>
  </Dialog>
</template>
