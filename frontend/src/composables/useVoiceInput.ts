import { ref } from 'vue'
import { VoiceRecorderStreamer } from '../utils/voiceStream'
import { toast } from 'vue-sonner'

export function useVoiceInput() {
  const recorder = new VoiceRecorderStreamer()
  const isRecording = ref(false)
  const isTranscribing = ref(false)
  const recordingStatus = ref<'idle' | 'recording' | 'transcribing'>('idle')
  const recordingDuration = ref(0)
  const currentVolume = ref(0)
  let durationTimer: number | null = null

  function formatDuration(sec: number): string {
    const m = Math.floor(sec / 60)
    const s = sec % 60
    return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
  }

  async function toggleVoiceInput(
    onTextUpdate: (delta: string, full: string) => void,
    onFinishCallback?: () => void
  ) {
    if (isRecording.value) {
      if (durationTimer) clearInterval(durationTimer)
      isRecording.value = false
      isTranscribing.value = true
      recordingStatus.value = 'transcribing'

      try {
        await recorder.stopRecordingAndTranscribe({
          onTranscribing: () => {
            isTranscribing.value = true
            recordingStatus.value = 'transcribing'
          },
          onDelta: (delta, full) => {
            onTextUpdate(delta, full)
          },
          onError: (err) => {
            toast.error(err.message || '语音识别出错')
            isTranscribing.value = false
            recordingStatus.value = 'idle'
          },
          onFinish: () => {
            isTranscribing.value = false
            recordingStatus.value = 'idle'
            if (onFinishCallback) onFinishCallback()
          }
        })
      } catch (err: any) {
        toast.error(err.message || '录音停止与转写失败')
        isTranscribing.value = false
        recordingStatus.value = 'idle'
      }
    } else {
      recordingDuration.value = 0
      try {
        await recorder.startRecording({
          onStart: () => {
            isRecording.value = true
            isTranscribing.value = false
            recordingStatus.value = 'recording'
            durationTimer = window.setInterval(() => {
              recordingDuration.value++
            }, 1000)
          },
          onError: (err) => {
            toast.error(err.message || '启动录音失败，请检查麦克风权限')
            isRecording.value = false
            recordingStatus.value = 'idle'
            if (durationTimer) clearInterval(durationTimer)
          }
        })
      } catch (err: any) {
        toast.error(err.message || '启动麦克风失败')
        isRecording.value = false
        recordingStatus.value = 'idle'
      }
    }
  }

  function cleanupVoice() {
    if (durationTimer) clearInterval(durationTimer)
    if (isRecording.value) {
      recorder.stopRecordingAndTranscribe({
        onError: () => {},
        onFinish: () => {}
      }).catch(() => {})
    }
  }

  return {
    isRecording,
    isTranscribing,
    recordingStatus,
    recordingDuration,
    currentVolume,
    formatDuration,
    toggleVoiceInput,
    cleanupVoice
  }
}
