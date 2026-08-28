<script setup lang="ts">
import {
  Info,
  ShieldCheck,
  Cpu,
  Layers,
  RadioTower,
  Globe,
  ExternalLink,
  Github,
  MessageSquare,
  Scale,
  Sparkles,
  Workflow,
  CheckCircle2
} from 'lucide-vue-next'
import Badge from '../ui/badge/Badge.vue'
import Button from '../ui/button/Button.vue'
</script>

<template>
  <div class="space-y-3 font-mono text-xs text-foreground select-none">
    <!-- Section Header -->
    <div class="flex items-center justify-between pb-1.5 border-b border-border/70">
      <div class="flex items-center gap-1.5">
        <Info class="w-3.5 h-3.5 text-primary" />
        <span class="text-xs font-bold font-mono text-foreground">关于 RelayMesh 与开源社区</span>
      </div>
      <div class="flex items-center gap-1.5">
        <Badge variant="outline" class="text-[9px] px-1.5 py-0 font-normal rounded-2xs bg-muted/60">
          Apache 2.0
        </Badge>
        <Badge variant="default" class="text-[9px] px-1.5 py-0 font-normal rounded-2xs font-mono">
          v1.0.0
        </Badge>
      </div>
    </div>

    <!-- 1. Hero Identity Card -->
    <div class="p-3.5 rounded-xs border border-border/70 bg-card/60 space-y-2.5">
      <div class="flex items-center justify-between gap-2">
        <div class="flex items-center gap-2.5">
          <div class="p-2 rounded-xs bg-primary text-primary-foreground shadow-2xs shrink-0 flex items-center justify-center">
            <Cpu class="w-4 h-4" />
          </div>
          <div>
            <div class="text-sm font-bold text-foreground tracking-tight flex items-center gap-1.5">
              <span>RelayMesh</span>
              <span class="text-[10px] text-muted-foreground font-normal font-sans">Agent Relay Hub</span>
            </div>
            <div class="text-[10px] text-muted-foreground">
              Go 1.25+ · Vue 3.5 · Tailwind CSS v4 · Pure Go SQLite (No Cgo)
            </div>
          </div>
        </div>

        <div class="hidden sm:flex items-center gap-2 text-[10px] text-muted-foreground">
          <span class="px-2 py-0.5 rounded-2xs bg-muted/70 border border-border/60">HTTP 18775</span>
          <span class="px-2 py-0.5 rounded-2xs bg-muted/70 border border-border/60">HTTPS 18776</span>
        </div>
      </div>

      <p class="text-[11px] text-muted-foreground leading-relaxed font-sans">
        RelayMesh 为 Cursor、Claude Code、Codex 及各类 AI Agent 提供高可靠人机协同中继网关。支持标准 MCP 协议、多轮工作流聚合调度、原生桌面浏览器自动唤醒与纯 Go 单二进制打包分发。
      </p>

      <!-- 3-Column Core Highlights -->
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-2 pt-1">
        <div class="p-2 rounded-2xs bg-background/80 border border-border/60 space-y-1">
          <div class="text-[10px] font-semibold text-foreground flex items-center gap-1">
            <RadioTower class="w-3 h-3 text-primary shrink-0" />
            <span>协议原生分流</span>
          </div>
          <p class="text-[9px] text-muted-foreground leading-normal font-sans">
            HTTP (18775) 承载 MCP/SSE，HTTPS (18776) 解锁浏览器录音权限。
          </p>
        </div>

        <div class="p-2 rounded-2xs bg-background/80 border border-border/60 space-y-1">
          <div class="text-[10px] font-semibold text-foreground flex items-center gap-1">
            <Workflow class="w-3 h-3 text-primary shrink-0" />
            <span>工作流多轮聚合</span>
          </div>
          <p class="text-[9px] text-muted-foreground leading-normal font-sans">
            按 Workflow 自动汇聚多轮交互，支持自定义重命名持久化锁定。
          </p>
        </div>

        <div class="p-2 rounded-2xs bg-background/80 border border-border/60 space-y-1">
          <div class="text-[10px] font-semibold text-foreground flex items-center gap-1">
            <ShieldCheck class="w-3 h-3 text-emerald-500 shrink-0" />
            <span>生产安全与极简</span>
          </div>
          <p class="text-[9px] text-muted-foreground leading-normal font-sans">
            内置原子反爆破限流、JWT 认证与纯 Go 单二进制内嵌资源。
          </p>
        </div>
      </div>
    </div>

    <!-- 2. Community & GitHub Guidance Grid -->
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
      <!-- GitHub Repo Card -->
      <div class="p-3 rounded-xs border border-border/70 bg-card/60 flex flex-col justify-between space-y-2">
        <div class="space-y-1">
          <div class="flex items-center gap-1.5 text-foreground font-bold">
            <Github class="w-3.5 h-3.5" />
            <span>GitHub 开源公共仓库</span>
          </div>
          <p class="text-[10px] text-muted-foreground leading-normal font-sans">
            欢迎 Star 关注项目演进、Fork 参与建设或查阅最新 Release 镜像与二进制产物。
          </p>
        </div>
        <div class="pt-1">
          <a
            href="https://github.com/teacat99/RelayMesh"
            target="_blank"
            rel="noopener noreferrer"
            class="inline-flex items-center gap-1 px-2.5 py-1 rounded-2xs text-[10px] font-mono bg-primary text-primary-foreground hover:opacity-90 transition-all font-medium cursor-pointer shadow-2xs"
          >
            <Github class="w-3 h-3" />
            <span>访问 GitHub 仓库</span>
            <ExternalLink class="w-2.5 h-2.5 ml-0.5 opacity-70" />
          </a>
        </div>
      </div>

      <!-- Feedback & Issues Card -->
      <div class="p-3 rounded-xs border border-border/70 bg-card/60 flex flex-col justify-between space-y-2">
        <div class="space-y-1">
          <div class="flex items-center gap-1.5 text-foreground font-bold">
            <MessageSquare class="w-3.5 h-3.5 text-primary" />
            <span>意见反馈与 Issue 跟踪</span>
          </div>
          <p class="text-[10px] text-muted-foreground leading-normal font-sans">
            如果您在部署或使用过程中遇到任何问题，欢迎随时提交 Bug 报告或需求建议。
          </p>
        </div>
        <div class="pt-1">
          <a
            href="https://github.com/teacat99/RelayMesh/issues"
            target="_blank"
            rel="noopener noreferrer"
            class="inline-flex items-center gap-1 px-2.5 py-1 rounded-2xs text-[10px] font-mono bg-muted border border-border/80 text-foreground hover:bg-muted/80 transition-all font-medium cursor-pointer"
          >
            <MessageSquare class="w-3 h-3 text-primary" />
            <span>提交 Issue / 需求建议</span>
            <ExternalLink class="w-2.5 h-2.5 ml-0.5 opacity-70" />
          </a>
        </div>
      </div>
    </div>

    <!-- 3. License Footer -->
    <div class="p-2.5 rounded-xs border border-border/50 bg-muted/30 flex items-center justify-between text-[10px] text-muted-foreground">
      <div class="flex items-center gap-1.5">
        <Scale class="w-3.5 h-3.5 text-primary shrink-0" />
        <span class="font-sans">开源许可证：<strong>Apache License 2.0</strong></span>
      </div>
      <span class="hidden sm:inline font-mono opacity-60">© 2026 RelayMesh</span>
    </div>
  </div>
</template>
