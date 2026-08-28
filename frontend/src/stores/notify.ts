import { defineStore } from 'pinia'
import { ref } from 'vue'

export const useNotifyStore = defineStore('notify', () => {
  const soundEnabled = ref(localStorage.getItem('relaymesh_sound') !== 'false')
  const desktopEnabled = ref(localStorage.getItem('relaymesh_desktop_notify') === 'true')

  function toggleSound() {
    soundEnabled.value = !soundEnabled.value
    localStorage.setItem('relaymesh_sound', soundEnabled.value ? 'true' : 'false')
  }

  function toggleDesktop() {
    if (!desktopEnabled.value && 'Notification' in window) {
      Notification.requestPermission().then(permission => {
        if (permission === 'granted') {
          desktopEnabled.value = true
          localStorage.setItem('relaymesh_desktop_notify', 'true')
        }
      })
    } else {
      desktopEnabled.value = false
      localStorage.setItem('relaymesh_desktop_notify', 'false')
    }
  }

  function playAlert() {
    if (!soundEnabled.value) return
    try {
      const ctx = new (window.AudioContext || (window as any).webkitAudioContext)()
      const osc = ctx.createOscillator()
      const gain = ctx.createGain()
      osc.type = 'sine'
      osc.frequency.setValueAtTime(587.33, ctx.currentTime) // D5
      osc.frequency.exponentialRampToValueAtTime(880, ctx.currentTime + 0.15) // A5
      gain.gain.setValueAtTime(0.15, ctx.currentTime)
      gain.gain.exponentialRampToValueAtTime(0.01, ctx.currentTime + 0.3)
      osc.connect(gain)
      gain.connect(ctx.destination)
      osc.start()
      osc.stop(ctx.currentTime + 0.3)
    } catch {
      // AudioContext not allowed or not supported
    }
  }

  function notify(title: string, body: string) {
    playAlert()
    if (desktopEnabled.value && 'Notification' in window && Notification.permission === 'granted') {
      new Notification(title, {
        body,
        icon: '/favicon.svg'
      })
    }
  }

  return {
    soundEnabled,
    desktopEnabled,
    toggleSound,
    toggleDesktop,
    notify,
    playAlert
  }
})
