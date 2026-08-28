import { onBeforeUnmount, watch, type Ref } from 'vue'

// useDialogBackGesture turns the system back gesture (or browser
// back button) into a "close the topmost dialog" action while any
// dialog is open. Without this, hitting Back inside a modal jumps the
// user to the previous route and dismisses the dialog as a side
// effect — confusing on mobile where the swipe gesture is the
// canonical "close this overlay" action.
//
// Mechanics:
//   1. When a dialog opens we pushState a clone of vue-router's
//      current state plus a sentinel marker. Cloning is required so
//      that vue-router still recognises the entry as "the same route"
//      and won't navigate away on the eventual popstate.
//   2. The shared popstate listener pops the LIFO stack of currently-
//      open dialogs and closes the topmost one. Because we do not
//      change the URL, vue-router stays put.
//   3. Programmatic close (Save / Cancel / X) consumes the matching
//      history entry via history.back(); we increment an "expected"
//      counter so the listener silently absorbs the resulting popstate
//      instead of cascading into a sibling dialog.
//
// The dialog ref the caller passes in is the same reactive value that
// drives v-model:open on the underlying radix DialogRoot, so opening
// and closing from anywhere stays in sync.

interface StackEntry {
  close: () => void
}

const stack: StackEntry[] = []
let listenerInstalled = false
// Number of popstate events we triggered ourselves and should silently
// swallow. Programmatic closes call history.back() asynchronously so a
// boolean flag isn't enough when nested dialogs unwind in a burst.
let expectedSelfPops = 0
const SENTINEL_KEY = '__portpassDialog__'

function installListenerOnce() {
  if (listenerInstalled || typeof window === 'undefined') return
  listenerInstalled = true
  window.addEventListener('popstate', () => {
    if (expectedSelfPops > 0) {
      expectedSelfPops -= 1
      return
    }
    const top = stack.pop()
    if (top) top.close()
  })
}

export function useDialogBackGesture(open: Ref<boolean>) {
  if (typeof window === 'undefined') return
  installListenerOnce()
  let entry: StackEntry | null = null

  watch(
    open,
    (v, prev) => {
      if (v && !prev) {
        entry = {
          close: () => {
            open.value = false
          }
        }
        stack.push(entry)
        try {
          // Preserve vue-router's existing state (back/current/forward/
          // position/scroll) so the eventual popstate doesn't make the
          // router think the user navigated to a different position.
          // Without this, vue-router's handler would treat the back as
          // a real route change and unwind to the previous route.
          const baseState = (window.history.state ?? {}) as Record<string, unknown>
          window.history.pushState({ ...baseState, [SENTINEL_KEY]: true }, '')
        } catch {
          // pushState can throw in sandboxed iframes; fail silently so
          // the dialog still works without the back-gesture polish.
        }
      } else if (!v && prev) {
        if (entry) {
          const idx = stack.indexOf(entry)
          if (idx >= 0) stack.splice(idx, 1)
        }
        const state = window.history.state as Record<string, unknown> | null
        if (state && state[SENTINEL_KEY]) {
          expectedSelfPops += 1
          window.history.back()
        }
        entry = null
      }
    },
    { flush: 'post' }
  )

  onBeforeUnmount(() => {
    if (!entry) return
    const idx = stack.indexOf(entry)
    if (idx >= 0) stack.splice(idx, 1)
    entry = null
  })
}
