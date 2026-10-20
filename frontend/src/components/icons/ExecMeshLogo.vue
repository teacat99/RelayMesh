<script setup lang="ts">
// ExecMesh 的图标：沿用 RelayMesh 的括号外框（同一个品牌家族），里面是终端提示符 >_ ——
// RelayMesh 里是智能体与人之间流动的会话（波形），ExecMesh 里是真正敲在远程主机上的命令。
defineProps<{
  class?: string
}>()
</script>

<template>
  <svg
    xmlns="http://www.w3.org/2000/svg"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.5"
    stroke-linecap="round"
    stroke-linejoin="round"
    :class="$props.class"
  >
    <path d="M8 4L6 4C5 4 4 5 4 6L4 10C4 11 3 12 2 12C3 12 4 13 4 14L4 18C4 19 5 20 6 20L8 20"/>
    <path d="M16 4L18 4C19 4 20 5 20 6L20 10C20 11 21 12 22 12C21 12 20 13 20 14L20 18C20 19 19 20 18 20L16 20"/>
    <path d="M8.8 9.2L12.2 12L8.8 14.8" stroke-width="2"/>
    <path d="M13.4 15.2L16.2 15.2" stroke-width="2"/>
  </svg>
</template>
