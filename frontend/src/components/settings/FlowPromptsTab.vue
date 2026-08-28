<script setup lang="ts">
import OnlineFlowDiagram from '../OnlineFlowDiagram.vue'
import FlowPromptsSettings from '../FlowPromptsSettings.vue'
import {
  FileText
} from 'lucide-vue-next'
</script>

<template>
  <div class="space-y-4">
    <!-- 流程流转状态图 -->
    <OnlineFlowDiagram />

    <!-- 提示词系统模板配置 -->
    <FlowPromptsSettings />
  </div>
</template>
