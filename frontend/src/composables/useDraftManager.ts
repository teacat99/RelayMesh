import { ref } from 'vue'
import type { SessionImage } from '../api/types'
import { draftsApi } from '../api/client'

export interface InputDraftItem {
  id: string
  text: string
  presets: string[]
  images: SessionImage[]
  updated_at: number
}

export interface WorkflowMultiDrafts {
  activeIndex: number
  drafts: InputDraftItem[]
}

export function useDraftManager() {
  const multiDrafts = ref<WorkflowMultiDrafts>({
    activeIndex: 0,
    drafts: [
      {
        id: '1',
        text: '',
        presets: [],
        images: [],
        updated_at: Date.now()
      }
    ]
  })

  const slideDirection = ref<'slide-left' | 'slide-right'>('slide-left')
  let isResetting = false
  let dbSaveTimer: number | null = null

  function getDraftStorageKey(workflowId: string): string {
    return `relaymesh_multi_drafts_wf_${workflowId || 'default'}`
  }

  function triggerDbSave(workflowId: string) {
    if (dbSaveTimer) {
      window.clearTimeout(dbSaveTimer)
    }
    dbSaveTimer = window.setTimeout(async () => {
      try {
        const payload = JSON.stringify(multiDrafts.value)
        await draftsApi.save(workflowId, multiDrafts.value.activeIndex, payload)
      } catch (e) {
        console.warn('Asynchronously saving workflow draft to database failed:', e)
      }
    }, 1000)
  }

  function saveDrafts(workflowId: string, currentText: string, currentPresets: string[], currentImages: SessionImage[]) {
    if (isResetting) return
    const wId = workflowId || 'default'
    const wKey = getDraftStorageKey(wId)

    const curIdx = multiDrafts.value.activeIndex
    if (multiDrafts.value.drafts[curIdx]) {
      multiDrafts.value.drafts[curIdx].text = currentText
      multiDrafts.value.drafts[curIdx].presets = currentPresets
      multiDrafts.value.drafts[curIdx].images = currentImages
      multiDrafts.value.drafts[curIdx].updated_at = Date.now()
    }

    try {
      localStorage.setItem(wKey, JSON.stringify(multiDrafts.value))
      localStorage.removeItem('relaymesh_active_draft_content')
      localStorage.removeItem('relaymesh_draft_global')
    } catch (_) {}

    triggerDbSave(wId)
  }

  async function loadDrafts(workflowId: string, onApplyDraft: (draft: InputDraftItem) => void) {
    if (isResetting) return
    const wId = workflowId || 'default'
    const wKey = getDraftStorageKey(wId)

    try {
      localStorage.removeItem('relaymesh_active_draft_content')
      localStorage.removeItem('relaymesh_draft_global')
    } catch (_) {}

    let loadedLocal = false
    try {
      const raw = localStorage.getItem(wKey)
      if (raw) {
        const data = JSON.parse(raw)
        if (data && Array.isArray(data.drafts) && data.drafts.length > 0) {
          multiDrafts.value = {
            activeIndex: typeof data.activeIndex === 'number' && data.activeIndex < data.drafts.length ? data.activeIndex : 0,
            drafts: data.drafts.slice(0, 5)
          }
          loadedLocal = true
        }
      }
    } catch (_) {}

    if (!loadedLocal) {
      multiDrafts.value = {
        activeIndex: 0,
        drafts: [
          {
            id: '1',
            text: '',
            presets: [],
            images: [],
            updated_at: Date.now()
          }
        ]
      }
    }

    const currentDraft = multiDrafts.value.drafts[multiDrafts.value.activeIndex] || multiDrafts.value.drafts[0]
    if (currentDraft) {
      onApplyDraft(currentDraft)
    }

    // Background asynchronous database synchronization
    try {
      const res = await draftsApi.get(wId)
      if (res && res.draft && res.draft.drafts_json) {
        const serverData = JSON.parse(res.draft.drafts_json)
        if (serverData && Array.isArray(serverData.drafts) && serverData.drafts.length > 0) {
          const localHasContent = multiDrafts.value.drafts.some(d => (d.text && d.text.trim().length > 0) || (d.images && d.images.length > 0))
          if (!localHasContent) {
            multiDrafts.value = {
              activeIndex: typeof serverData.activeIndex === 'number' && serverData.activeIndex < serverData.drafts.length ? serverData.activeIndex : 0,
              drafts: serverData.drafts.slice(0, 5)
            }
            try {
              localStorage.setItem(wKey, JSON.stringify(multiDrafts.value))
            } catch (_) {}
            const activeD = multiDrafts.value.drafts[multiDrafts.value.activeIndex] || multiDrafts.value.drafts[0]
            if (activeD) {
              onApplyDraft(activeD)
            }
          }
        }
      }
    } catch (err) {
      console.warn('Failed to load workflow draft from database:', err)
    }
  }

  function addNewDraft(workflowId: string, currentText: string, currentPresets: string[], currentImages: SessionImage[], onApply: (d: InputDraftItem) => void) {
    if (multiDrafts.value.drafts.length >= 5) return
    saveDrafts(workflowId, currentText, currentPresets, currentImages)
    const newId = String(Date.now())
    const newDraft: InputDraftItem = {
      id: newId,
      text: '',
      presets: [],
      images: [],
      updated_at: Date.now()
    }
    slideDirection.value = 'slide-left'
    multiDrafts.value.drafts.push(newDraft)
    multiDrafts.value.activeIndex = multiDrafts.value.drafts.length - 1
    onApply(newDraft)
    saveDrafts(workflowId, '', [], [])
  }

  function switchDraft(targetIndex: number, workflowId: string, currentText: string, currentPresets: string[], currentImages: SessionImage[], onApply: (d: InputDraftItem) => void) {
    if (targetIndex === multiDrafts.value.activeIndex || targetIndex < 0 || targetIndex >= multiDrafts.value.drafts.length) return
    saveDrafts(workflowId, currentText, currentPresets, currentImages)
    slideDirection.value = targetIndex > multiDrafts.value.activeIndex ? 'slide-left' : 'slide-right'
    multiDrafts.value.activeIndex = targetIndex
    const d = multiDrafts.value.drafts[targetIndex]
    if (d) {
      onApply(d)
    }
    saveDrafts(workflowId, d.text || '', d.presets || [], d.images || [])
  }

  function deleteCurrentDraft(workflowId: string, onApply: (d: InputDraftItem) => void) {
    const total = multiDrafts.value.drafts.length
    if (total <= 1) {
      const d = multiDrafts.value.drafts[0]
      d.text = ''
      d.presets = []
      d.images = []
      d.updated_at = Date.now()
      onApply(d)
      saveDrafts(workflowId, '', [], [])
      return
    }

    const curIdx = multiDrafts.value.activeIndex
    multiDrafts.value.drafts.splice(curIdx, 1)
    slideDirection.value = 'slide-right'
    multiDrafts.value.activeIndex = Math.max(0, curIdx - 1)
    const newD = multiDrafts.value.drafts[multiDrafts.value.activeIndex]
    onApply(newD)
    saveDrafts(workflowId, newD.text || '', newD.presets || [], newD.images || [])
  }

  function resetFormState(workflowId: string, onClear: () => void) {
    isResetting = true
    const wId = workflowId || 'default'
    const wKey = getDraftStorageKey(wId)
    
    multiDrafts.value = {
      activeIndex: 0,
      drafts: [
        {
          id: '1',
          text: '',
          presets: [],
          images: [],
          updated_at: Date.now()
        }
      ]
    }
    onClear()

    try {
      localStorage.removeItem(wKey)
      localStorage.removeItem(`relaymesh_draft_wf_${wId}`)
      localStorage.removeItem('relaymesh_active_draft_content')
      localStorage.removeItem('relaymesh_draft_global')
    } catch (_) {}

    try {
      draftsApi.save(wId, 0, JSON.stringify(multiDrafts.value)).catch(() => {})
    } catch (_) {}

    setTimeout(() => {
      isResetting = false
    }, 200)
  }

  return {
    multiDrafts,
    slideDirection,
    saveDrafts,
    loadDrafts,
    addNewDraft,
    switchDraft,
    deleteCurrentDraft,
    resetFormState
  }
}
