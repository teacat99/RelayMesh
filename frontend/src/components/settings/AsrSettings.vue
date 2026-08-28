<script setup lang="ts">
import { ref } from 'vue'
import { useSettingsStore } from '@/stores/settings'
import { VoiceRecorderStreamer } from '@/utils/voiceStream'
import {
  Mic,
  MicOff,
  RotateCcw,
  Key,
  Eye,
  EyeOff,
  Activity,
  Radio,
  CheckCircle2,
  SlidersHorizontal,
  AlertTriangle
} from 'lucide-vue-next'

const settingsStore = useSettingsStore()
const showApiKey = ref(false)

function handleInputSave(key: string, val: any) {
  settingsStore.updateSettings({ [key]: val })
}

function resetAsrSettings() {
  settingsStore.updateSettings({
    asrProvider: 'mimo',
    asrApiUrl: 'https://api.xiaomimimo.com/v1/chat/completions',
    asrApiKey: '',
    asrModel: 'mimo-v2.5-asr',
    asrLanguage: 'auto',
    asrStream: true
  })
}

// Voice Test in Settings
const testRecorder = new VoiceRecorderStreamer()
const isTestingVoice = ref(false)
const testRecordingStatus = ref<'idle' | 'recording' | 'transcribing'>('idle')
const testTranscriptionResult = ref('')
const testErrorMsg = ref('')
let webSpeechTestRec: any = null

async function toggleTestVoice() {
  const provider = settingsStore.settings.asrProvider || 'mimo'

  // 1. WebSpeech 引擎实测
  if (provider === 'webspeech') {
    if (isTestingVoice.value) {
      if (webSpeechTestRec) {
        try { webSpeechTestRec.stop() } catch (_) {}
        webSpeechTestRec = null
      }
      isTestingVoice.value = false
      testRecordingStatus.value = 'idle'
    } else {
      testTranscriptionResult.value = ''
      testErrorMsg.value = ''
      const SpeechRecognition = (window as any).SpeechRecognition || (window as any).webkitSpeechRecognition
      if (!SpeechRecognition) {
        testErrorMsg.value = '当前浏览器不支持 Web Speech API，建议使用 Chrome/Edge 访问。'
        return
      }
      try {
        const rec = new SpeechRecognition()
        rec.continuous = true
        rec.interimResults = true
        rec.lang = settingsStore.settings.speechLang || 'zh-CN'
        rec.onstart = () => {
          isTestingVoice.value = true
          testRecordingStatus.value = 'recording'
        }
        rec.onresult = (event: any) => {
          let interim = ''
          let final = ''
          for (let i = event.resultIndex; i < event.results.length; ++i) {
            if (event.results[i].isFinal) final += event.results[i][0].transcript
            else interim += event.results[i][0].transcript
          }
          testTranscriptionResult.value = final || interim
        }
        rec.onerror = (event: any) => {
          if (event.error !== 'no-speech') {
            testErrorMsg.value = `WebSpeech 识别错误: ${event.error}`
            isTestingVoice.value = false
            testRecordingStatus.value = 'idle'
          }
        }
        rec.onend = () => {
          isTestingVoice.value = false
          testRecordingStatus.value = 'idle'
        }
        webSpeechTestRec = rec
        rec.start()
      } catch (err: any) {
        testErrorMsg.value = err.message || '启动 WebSpeech 测试失败'
        isTestingVoice.value = false
        testRecordingStatus.value = 'idle'
      }
    }
    return
  }

  // 2. Xiaomi MIMO 引擎实测
  if (isTestingVoice.value) {
    testRecordingStatus.value = 'transcribing'
    try {
      await testRecorder.stopRecordingAndTranscribe({
        onTranscribing: () => {
          testRecordingStatus.value = 'transcribing'
        },
        onDelta: (_delta, full) => {
          testTranscriptionResult.value = full
        },
        onError: (err) => {
          const raw = err.message || String(err)
          if (raw.includes('401') || raw.includes('Invalid API Key')) {
            testErrorMsg.value = '转录失败 (401 密钥无效)：Xiaomi MIMO API Key 错误或未授权，请检查输入的密钥。'
          } else {
            testErrorMsg.value = raw || '录音/转录测试出错'
          }
          testRecordingStatus.value = 'idle'
          isTestingVoice.value = false
        },
        onFinish: (final) => {
          testTranscriptionResult.value = final || '(未识别到声音)'
          testRecordingStatus.value = 'idle'
          isTestingVoice.value = false
        }
      })
    } catch (e: any) {
      testErrorMsg.value = e.message || '转录失败'
      testRecordingStatus.value = 'idle'
      isTestingVoice.value = false
    }
  } else {
    testTranscriptionResult.value = ''
    testErrorMsg.value = ''

    if (!settingsStore.settings.asrApiKey || !settingsStore.settings.asrApiKey.trim()) {
      testErrorMsg.value = '未填写 Xiaomi MiMo API Key，请先在上方输入有效 API Key 或切换为「浏览器原生 WebSpeech」免 Key 模式'
      return
    }

    try {
      await testRecorder.startRecording({
        onStart: () => {
          isTestingVoice.value = true
          testRecordingStatus.value = 'recording'
        },
        onError: (err) => {
          testErrorMsg.value = err.message || '麦克风权限或初始化失败'
          isTestingVoice.value = false
          testRecordingStatus.value = 'idle'
        }
      })
    } catch (e: any) {
      testErrorMsg.value = e.message || '启动录音失败'
      isTestingVoice.value = false
      testRecordingStatus.value = 'idle'
    }
  }
}

const asrLanguageOptions = [
  { label: '自动识别', value: 'auto' },
  { label: '中文 (普通话)', value: 'zh' },
  { label: '英文 (English)', value: 'en' },
  { label: '粤语 (Cantonese)', value: 'yue' }
]
</script>

<template>
  <div class="space-y-4">
    <!-- Section Header -->
    <div class="flex items-center justify-between pb-1.5 border-b border-border/70">
      <div class="flex items-center gap-1.5">
        <Mic class="w-3.5 h-3.5 text-primary" />
        <span class="text-xs font-bold font-mono text-foreground">语音输入与 ASR 模型引擎</span>
      </div>
      <button
        type="button"
        class="text-[10px] font-mono text-muted-foreground hover:text-foreground underline underline-offset-2 flex items-center gap-1 cursor-pointer transition-colors"
        @click="resetAsrSettings"
      >
        <RotateCcw class="w-2.5 h-2.5" />
        <span>恢复默认</span>
      </button>
    </div>

    <!-- Provider Selection -->
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
      <button
        type="button"
        class="p-2.5 rounded-xs border text-left transition-all font-mono cursor-pointer flex items-center justify-between"
        :class="(settingsStore.settings.asrProvider || 'mimo') === 'mimo'
          ? 'border-primary bg-primary/10 text-foreground font-bold shadow-2xs'
          : 'border-border/70 bg-card/60 hover:bg-muted text-muted-foreground'"
        @click="handleInputSave('asrProvider', 'mimo')"
      >
        <div class="space-y-0.5">
          <div class="text-xs font-semibold text-foreground">Xiaomi MiMo ASR (推荐)</div>
          <div class="text-[10px] text-muted-foreground">超低延迟流式转写，精准识别中英文</div>
        </div>
        <CheckCircle2 v-if="(settingsStore.settings.asrProvider || 'mimo') === 'mimo'" class="w-4 h-4 text-primary shrink-0" />
      </button>

      <button
        type="button"
        class="p-2.5 rounded-xs border text-left transition-all font-mono cursor-pointer flex items-center justify-between"
        :class="settingsStore.settings.asrProvider === 'webspeech'
          ? 'border-primary bg-primary/10 text-foreground font-bold shadow-2xs'
          : 'border-border/70 bg-card/60 hover:bg-muted text-muted-foreground'"
        @click="handleInputSave('asrProvider', 'webspeech')"
      >
        <div class="space-y-0.5">
          <div class="text-xs font-semibold text-foreground">浏览器原生 WebSpeech ASR</div>
          <div class="text-[10px] text-muted-foreground">免配置 Key，直接调用浏览器识别引擎</div>
        </div>
        <CheckCircle2 v-if="settingsStore.settings.asrProvider === 'webspeech'" class="w-4 h-4 text-primary shrink-0" />
      </button>
    </div>

    <!-- Form Configuration -->
    <div class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-3">
      <!-- API Endpoint -->
      <div class="space-y-1">
        <label class="text-xs font-mono font-medium text-foreground flex items-center justify-between">
          <span>API 端点 URL (Endpoint)</span>
        </label>
        <input
          type="text"
          :value="settingsStore.settings.asrApiUrl || ''"
          placeholder="https://api.xiaomimimo.com/v1/chat/completions"
          class="w-full h-8 px-2.5 text-xs font-mono bg-background border border-border/80 rounded-xs focus:outline-none focus:border-primary text-foreground"
          @input="(e) => handleInputSave('asrApiUrl', (e.target as HTMLInputElement).value.trim())"
          @change="(e) => handleInputSave('asrApiUrl', (e.target as HTMLInputElement).value.trim())"
        />
      </div>

      <!-- API Key -->
      <div class="space-y-1">
        <label class="text-xs font-mono font-medium text-foreground flex items-center justify-between">
          <span class="flex items-center gap-1">
            <Key class="w-3 h-3 text-primary" />
            <span>API 访问密钥 (API Key)</span>
          </span>
          <button
            type="button"
            class="text-[10px] text-muted-foreground hover:text-foreground flex items-center gap-0.5 cursor-pointer"
            @click="showApiKey = !showApiKey"
          >
            <component :is="showApiKey ? EyeOff : Eye" class="w-3 h-3" />
            <span>{{ showApiKey ? '隐藏' : '显示' }}</span>
          </button>
        </label>
        <input
          :type="showApiKey ? 'text' : 'password'"
          :value="settingsStore.settings.asrApiKey || ''"
          placeholder="sk-..."
          class="w-full h-8 px-2.5 text-xs font-mono bg-background border border-border/80 rounded-xs focus:outline-none focus:border-primary text-foreground"
          @input="(e) => handleInputSave('asrApiKey', (e.target as HTMLInputElement).value.trim())"
          @change="(e) => handleInputSave('asrApiKey', (e.target as HTMLInputElement).value.trim())"
        />
        <!-- Missing Key Warning Hint -->
        <div
          v-if="(settingsStore.settings.asrProvider || 'mimo') === 'mimo' && (!settingsStore.settings.asrApiKey || !settingsStore.settings.asrApiKey.trim())"
          class="p-2 rounded-xs bg-amber-500/10 border border-amber-500/30 text-amber-600 dark:text-amber-400 text-[10px] font-mono flex items-start gap-1.5 mt-1"
        >
          <AlertTriangle class="w-3.5 h-3.5 shrink-0 mt-0.5 text-amber-500" />
          <div class="space-y-0.5">
            <div class="font-bold">Xiaomi MiMo 需提供有效 API Key</div>
            <div class="text-[9px] text-muted-foreground font-sans leading-normal">
              使用 MiMo ASR 需填写对应平台 API Key；若暂无 Key，可点击上方卡片一键切换为「浏览器原生 WebSpeech ASR」免配置模式。
            </div>
          </div>
        </div>
      </div>

      <!-- Model Name & Language -->
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
        <div class="space-y-1">
          <label class="text-xs font-mono font-medium text-foreground">模型名称 (Model)</label>
          <input
            type="text"
            :value="settingsStore.settings.asrModel || 'mimo-v2.5-asr'"
            placeholder="mimo-v2.5-asr"
            class="w-full h-8 px-2.5 text-xs font-mono bg-background border border-border/80 rounded-xs focus:outline-none focus:border-primary text-foreground"
            @input="(e) => handleInputSave('asrModel', (e.target as HTMLInputElement).value.trim())"
            @change="(e) => handleInputSave('asrModel', (e.target as HTMLInputElement).value.trim())"
          />
        </div>

        <div class="space-y-1">
          <label class="text-xs font-mono font-medium text-foreground">识别语言 (Language)</label>
          <select
            :value="settingsStore.settings.asrLanguage || 'auto'"
            class="w-full h-8 px-2 text-xs font-mono bg-background border border-border/80 rounded-xs focus:outline-none focus:border-primary text-foreground cursor-pointer"
            @change="(e) => handleInputSave('asrLanguage', (e.target as HTMLSelectElement).value)"
          >
            <option v-for="opt in asrLanguageOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </option>
          </select>
        </div>
      </div>
    </div>

    <!-- Voice Test Box -->
    <div class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-2.5">
      <div class="flex items-center justify-between text-xs font-mono font-medium text-foreground">
        <span class="flex items-center gap-1.5">
          <Activity class="w-3.5 h-3.5 text-primary" />
          <span>麦克风与转录快速实测</span>
        </span>
        <button
          type="button"
          class="px-2.5 py-1 rounded-xs text-xs font-mono font-bold flex items-center gap-1.5 cursor-pointer transition-all border"
          :class="isTestingVoice
            ? 'bg-destructive text-destructive-foreground border-destructive animate-pulse'
            : 'bg-primary text-primary-foreground border-primary hover:opacity-90'"
          @click="toggleTestVoice"
        >
          <component :is="isTestingVoice ? MicOff : Mic" class="w-3.5 h-3.5" />
          <span>{{ isTestingVoice ? '停止录音并测试' : '点击开始测试' }}</span>
        </button>
      </div>

      <div v-if="testRecordingStatus === 'recording'" class="p-2.5 rounded-xs bg-destructive/10 border border-destructive/20 text-xs font-mono text-destructive flex items-center gap-2">
        <span class="w-2 h-2 rounded-full bg-destructive animate-ping"></span>
        <span>正在录音中... 请对着麦克风说话，完毕后点击上方停止</span>
      </div>

      <div v-if="testTranscriptionResult" class="p-2.5 rounded-xs bg-background border border-border/80 text-xs font-mono text-foreground space-y-1">
        <div class="text-[10px] text-muted-foreground">识别结果：</div>
        <div class="font-sans text-xs leading-relaxed text-foreground">{{ testTranscriptionResult }}</div>
      </div>

      <div v-if="testErrorMsg" class="p-2 rounded-xs bg-destructive/10 border border-destructive/20 text-xs font-mono text-destructive">
        {{ testErrorMsg }}
      </div>
    </div>
  </div>
</template>
