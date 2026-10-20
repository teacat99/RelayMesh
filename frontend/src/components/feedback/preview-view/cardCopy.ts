// 预览视图里各种卡片的「复制」动作（#120 · D-273）：写剪贴板，成功 / 失败各给一条提示。
// label 是被复制内容的名字（「正文」「会话号」「提交哈希」…），拼进提示里。
import { toast } from '@/components/ui/sonner'
import { copyText } from '../../../lib/terminal/clipboard'

export async function copyWithToast(text: string, label: string): Promise<void> {
  if (!text) return
  if (await copyText(text)) toast.success(`已复制${label}`, { duration: 1800 })
  else toast.error(`复制${label}失败，请手动选择文本`)
}
