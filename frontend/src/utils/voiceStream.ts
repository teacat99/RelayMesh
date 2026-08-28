import { useSettingsStore } from '../stores/settings'

export interface VoiceStreamCallbacks {
  onStart?: () => void
  onRecording?: () => void
  onTranscribing?: () => void
  onDelta?: (deltaText: string, fullText: string) => void
  onError?: (err: any) => void
  onFinish?: (finalText: string) => void
}

export class VoiceRecorderStreamer {
  private mediaStream: MediaStream | null = null
  private mediaRecorder: MediaRecorder | null = null
  private audioChunks: Blob[] = []
  private isRecording = false
  private abortController: AbortController | null = null

  async startRecording(callbacks: VoiceStreamCallbacks): Promise<void> {
    try {
      this.audioChunks = []
      this.isRecording = true
      this.abortController = new AbortController()

      if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
        throw new Error('当前浏览器不支持麦克风录音访问 (MediaDevices API)')
      }

      const stream = await navigator.mediaDevices.getUserMedia({ audio: true })
      this.mediaStream = stream

      let mimeType = 'audio/webm'
      if (MediaRecorder.isTypeSupported('audio/webm;codecs=opus')) {
        mimeType = 'audio/webm;codecs=opus'
      } else if (MediaRecorder.isTypeSupported('audio/mp4')) {
        mimeType = 'audio/mp4'
      } else if (MediaRecorder.isTypeSupported('audio/ogg')) {
        mimeType = 'audio/ogg'
      }

      const recorder = new MediaRecorder(stream, { mimeType })
      this.mediaRecorder = recorder

      recorder.ondataavailable = (event) => {
        if (event.data && event.data.size > 0) {
          this.audioChunks.push(event.data)
        }
      }

      recorder.onstart = () => {
        callbacks.onStart?.()
        callbacks.onRecording?.()
      }

      recorder.onerror = (e) => {
        this.stopStream()
        callbacks.onError?.(e)
      }

      recorder.start(250) // 250ms chunks
    } catch (err) {
      this.stopStream()
      callbacks.onError?.(err)
      throw err
    }
  }

  async stopRecordingAndTranscribe(callbacks: VoiceStreamCallbacks): Promise<string> {
    return new Promise((resolve, reject) => {
      if (!this.mediaRecorder || this.mediaRecorder.state === 'inactive') {
        this.stopStream()
        resolve('')
        return
      }

      this.mediaRecorder.onstop = async () => {
        try {
          callbacks.onTranscribing?.()
          const mimeType = this.mediaRecorder?.mimeType || 'audio/webm'
          const audioBlob = new Blob(this.audioChunks, { type: mimeType })
          this.stopStream()

          if (audioBlob.size === 0) {
            resolve('')
            callbacks.onFinish?.('')
            return
          }

          // Convert audio blob to base64
          const base64Audio = await this.blobToBase64(audioBlob)

          const settingsStore = useSettingsStore()
          const settings = settingsStore.settings

          const payload = {
            audio_base64: base64Audio,
            mime_type: mimeType,
            api_url: settings.asrApiUrl || 'https://api.xiaomimimo.com/v1/chat/completions',
            api_key: settings.asrApiKey || '',
            model: settings.asrModel || 'mimo-v2.5-asr',
            language: settings.asrLanguage || 'auto',
            stream: settings.asrStream !== false
          }

          // Send POST request to backend proxy
          const response = await fetch('/api/v1/voice/transcribe', {
            method: 'POST',
            headers: {
              'Content-Type': 'application/json'
            },
            body: JSON.stringify(payload),
            signal: this.abortController?.signal
          })

          if (!response.ok) {
            const errText = await response.text()
            throw new Error(`语音识别接口请求失败 (${response.status}): ${errText}`)
          }

          let fullAccumulated = ''

          if (payload.stream && response.body) {
            const reader = response.body.getReader()
            const decoder = new TextDecoder('utf-8')
            let buffer = ''

            while (true) {
              const { done, value } = await reader.read()
              if (done) break

              buffer += decoder.decode(value, { stream: true })
              const lines = buffer.split('\n')
              buffer = lines.pop() || ''

              for (const line of lines) {
                const trimmed = line.trim()
                if (!trimmed || trimmed.startsWith(':')) continue
                if (trimmed === 'data: [DONE]') continue

                if (trimmed.startsWith('data:')) {
                  const jsonStr = trimmed.slice(5).trim()
                  try {
                    const parsed = JSON.parse(jsonStr)
                    const delta = parsed.choices?.[0]?.delta?.content || parsed.text || ''
                    if (delta) {
                      fullAccumulated += delta
                      callbacks.onDelta?.(delta, fullAccumulated)
                    }
                  } catch (e) {
                    // Ignore SSE json parse error
                  }
                }
              }
            }
          } else {
            const data = await response.json()
            fullAccumulated = data.choices?.[0]?.message?.content || data.text || ''
            callbacks.onDelta?.(fullAccumulated, fullAccumulated)
          }

          callbacks.onFinish?.(fullAccumulated)
          resolve(fullAccumulated)
        } catch (err) {
          callbacks.onError?.(err)
          reject(err)
        }
      }

      this.mediaRecorder.stop()
    })
  }

  cancel() {
    this.abortController?.abort()
    if (this.mediaRecorder && this.mediaRecorder.state !== 'inactive') {
      try {
        this.mediaRecorder.stop()
      } catch (_) {}
    }
    this.stopStream()
  }

  private stopStream() {
    this.isRecording = false
    if (this.mediaStream) {
      this.mediaStream.getTracks().forEach(track => track.stop())
      this.mediaStream = null
    }
    this.mediaRecorder = null
    this.audioChunks = []
  }

  private blobToBase64(blob: Blob): Promise<string> {
    return new Promise((resolve, reject) => {
      const reader = new FileReader()
      reader.onloadend = () => {
        const res = reader.result as string
        const base64 = res.split(',')[1] || res
        resolve(base64)
      }
      reader.onerror = reject
      reader.readAsDataURL(blob)
    })
  }
}
