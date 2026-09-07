<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription
} from '../ui/sheet'
import Button from '../ui/button/Button.vue'
import Badge from '../ui/badge/Badge.vue'
import MarkdownRenderer from '../MarkdownRenderer.vue'
import { workflowSheetApi } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import {
  FileText,
  RefreshCw,
  Copy,
  Edit3,
  Eye,
  Save,
  Check,
  Sparkles
} from 'lucide-vue-next'
import { toast } from 'vue-sonner'

const props = defineProps<{
  open: boolean
  workflowId?: string
}>()

const emit = defineEmits<{
  (e: 'update:open', val: boolean): void
}>()

const sessionStore = useSessionStore()

const loading = ref(false)
const saving = ref(false)
const mode = ref<'preview' | 'edit'>('preview')
const content = ref('')
const editContent = ref('')
const updatedAt = ref<string | null>(null)
const copied = ref(false)

const effectiveWorkflowId = computed(() => {
  return props.workflowId || sessionStore.selectedSession?.workflow_id || sessionStore.currentSession?.workflow_id || ''
})

async function fetchSheet(silent = false) {
  if (!effectiveWorkflowId.value) {
    content.value = ''
    editContent.value = ''
    updatedAt.value = null
    return
  }
  if (!silent) loading.value = true
  try {
    const res = await workflowSheetApi.get(effectiveWorkflowId.value)
    content.value = res.content || ''
    editContent.value = res.content || ''
    updatedAt.value = res.updated_at || null
  } catch (err: any) {
    if (!silent) {
      toast.error('加载工作表文档失败', {
        description: err?.response?.data?.error || err.message
      })
    }
  } finally {
    if (!silent) loading.value = false
  }
}

watch(
  () => [props.open, effectiveWorkflowId.value],
  ([isOpen]) => {
    if (isOpen) {
      mode.value = 'preview'
      fetchSheet()
    }
  },
  { immediate: true }
)

// Watch SSE real-time push for workflow_sheet_updated
watch(
  () => sessionStore.lastSheetEvent,
  (evt) => {
    if (props.open && evt?.workflow_id && evt.workflow_id === effectiveWorkflowId.value) {
      fetchSheet(true)
    }
  }
)

function toggleMode() {
  if (mode.value === 'preview') {
    editContent.value = content.value
    mode.value = 'edit'
  } else {
    mode.value = 'preview'
  }
}

async function handleSave() {
  if (!effectiveWorkflowId.value) return
  saving.value = true
  try {
    await workflowSheetApi.save(effectiveWorkflowId.value, editContent.value)
    content.value = editContent.value
    updatedAt.value = new Date().toISOString()
    mode.value = 'preview'
    toast.success('工作表文档已保存')
  } catch (err: any) {
    toast.error('保存失败', {
      description: err?.response?.data?.error || err.message
    })
  } finally {
    saving.value = false
  }
}

async function copyDocument() {
  const text = content.value || editContent.value
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    copied.value = true
    toast.success('已复制工作表 Markdown 内容')
    setTimeout(() => { copied.value = false }, 2000)
  } catch (_) {
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
    copied.value = true
    toast.success('已复制工作表 Markdown 内容')
    setTimeout(() => { copied.value = false }, 2000)
  }
}

function handleInitializeTemplate() {
  const tpl = `# 工作表 · ${effectiveWorkflowId.value}

## 1. 任务目标与范围
- **核心目标**：
- **约束与边界**：

## 2. 关键决策记录
| ID | 决策项 | 理由 | 状态 |
|---|---|---|---|
| D-01 | | | 现行 |

## 3. 技术锚点与规范
- 治理规范：遵循 RelayMesh 内生治理指引与工程思维 (C-PLAN / C-RT)
- 场景模式：online (默认) / away (兜底) / autopilot (编排)

## 4. 阶段演变与变更备忘
- [ ] Phase 0 需求收集与澄清
- [ ] Phase 2 方案评审
- [ ] Phase 4 开发执行与增量验证
- [ ] Phase 6 部署验证
- [ ] Phase 7 归档收尾
`
  editContent.value = tpl
  mode.value = 'edit'
}

function formatTime(isoStr?: string | null) {
  if (!isoStr) return '暂未记录'
  const d = new Date(isoStr)
  if (isNaN(d.getTime())) return isoStr
  return d.toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  })
}
</script>

<template>
  <Sheet :open="props.open" @update:open="(val: boolean) => emit('update:open', val)">
    <SheetContent
      side="right"
      class="w-full sm:max-w-xl md:max-w-2xl lg:max-w-3xl flex flex-col p-0 gap-0 border-l border-border bg-card shadow-2xl z-50 overflow-hidden"
    >
      <!-- Header -->
      <SheetHeader class="px-5 py-4 border-b border-border/80 bg-muted/20 shrink-0">
        <div class="flex items-center justify-between pr-8">
          <div class="space-y-1">
            <div class="flex items-center gap-2">
              <FileText class="w-4 h-4 text-primary shrink-0" />
              <SheetTitle class="text-sm font-semibold tracking-tight text-foreground">
                会话工作表 · Workflow Sheet
              </SheetTitle>
              <Badge variant="outline" class="font-mono text-[10px] px-1.5 py-0 h-4">
                {{ effectiveWorkflowId || '未绑定工作流' }}
              </Badge>
            </div>
            <SheetDescription class="text-xs text-muted-foreground flex items-center gap-2">
              <span>用于跨上下文压缩保留目标、关键决策与技术锚点</span>
              <span v-if="updatedAt" class="font-mono text-[11px] text-muted-foreground/80">
                · 更新于 {{ formatTime(updatedAt) }}
              </span>
            </SheetDescription>
          </div>
        </div>

        <!-- Action Toolbar -->
        <div class="flex items-center justify-between pt-2">
          <div class="flex items-center gap-1.5">
            <Button
              size="sm"
              variant="outline"
              class="h-7 text-xs font-normal"
              :disabled="loading"
              @click="toggleMode"
            >
              <component :is="mode === 'preview' ? Edit3 : Eye" class="w-3.5 h-3.5 mr-1" />
              {{ mode === 'preview' ? '编辑文档' : '预览文档' }}
            </Button>

            <Button
              v-if="mode === 'edit'"
              size="sm"
              variant="default"
              class="h-7 text-xs font-normal"
              :disabled="saving"
              @click="handleSave"
            >
              <Save class="w-3.5 h-3.5 mr-1" />
              {{ saving ? '保存中...' : '保存' }}
            </Button>

            <Button
              size="sm"
              variant="ghost"
              class="h-7 text-xs font-normal"
              :disabled="!content && !editContent"
              @click="copyDocument"
            >
              <component :is="copied ? Check : Copy" class="w-3.5 h-3.5 mr-1" :class="{ 'text-green-500': copied }" />
              {{ copied ? '已复制' : '复制' }}
            </Button>
          </div>

          <div class="flex items-center gap-1">
            <Button
              size="sm"
              variant="ghost"
              class="h-7 px-2 text-xs font-normal"
              :disabled="loading"
              title="刷新文档"
              @click="() => fetchSheet()"
            >
              <RefreshCw class="w-3.5 h-3.5" :class="{ 'animate-spin': loading }" />
            </Button>
          </div>
        </div>
      </SheetHeader>

      <!-- Main Body -->
      <div class="flex-1 overflow-y-auto min-h-0 relative p-5">
        <!-- Loading State -->
        <div v-if="loading" class="flex flex-col items-center justify-center py-20 text-muted-foreground gap-3">
          <RefreshCw class="w-6 h-6 animate-spin text-primary" />
          <p class="text-xs font-mono">正在检索工作表文档...</p>
        </div>

        <!-- Empty State -->
        <div
          v-else-if="!content && mode === 'preview'"
          class="flex flex-col items-center justify-center py-16 px-4 text-center space-y-4 max-w-md mx-auto"
        >
          <div class="w-12 h-12 rounded-full bg-muted/80 flex items-center justify-center border border-border">
            <Sparkles class="w-6 h-6 text-muted-foreground" />
          </div>
          <div class="space-y-1.5">
            <h3 class="text-sm font-semibold text-foreground">暂无内置工作表文档</h3>
            <p class="text-xs text-muted-foreground leading-relaxed">
              当项目无本地文件体系时，智能体可通过 <code class="font-mono text-primary bg-muted px-1 py-0.5 rounded text-[11px]">workflow_context(action: 'session_doc_save')</code>
              将目标、关键决策与技术锚点持久化至此。
            </p>
          </div>
          <div class="flex items-center gap-2 pt-2">
            <Button size="sm" variant="outline" class="text-xs" @click="handleInitializeTemplate">
              <Edit3 class="w-3.5 h-3.5 mr-1.5" />
              写入标准模板
            </Button>
          </div>
        </div>

        <!-- Markdown Preview Mode -->
        <div v-else-if="mode === 'preview'" class="prose prose-sm dark:prose-invert max-w-none">
          <MarkdownRenderer :content="content" />
        </div>

        <!-- Edit Mode -->
        <div v-else class="h-full flex flex-col gap-2">
          <textarea
            v-model="editContent"
            rows="24"
            class="flex-1 w-full p-4 font-mono text-xs rounded-sm border border-border bg-background focus:outline-none focus:ring-1 focus:ring-primary resize-none leading-relaxed"
            placeholder="请输入 Markdown 格式的会话状态文档与关键决策..."
          ></textarea>
        </div>
      </div>

      <!-- Footer Info -->
      <div class="px-5 py-2.5 border-t border-border/60 bg-muted/10 shrink-0 flex items-center justify-between text-[11px] text-muted-foreground font-mono">
        <span>载体：RelayMesh DB (WorkflowNote[note_key="session_doc"])</span>
        <span v-if="mode === 'edit'">字符数：{{ editContent.length }}</span>
      </div>
    </SheetContent>
  </Sheet>
</template>
