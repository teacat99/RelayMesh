<script setup lang="ts">
import AppLayout from './layouts/AppLayout.vue'
import { Toaster } from './components/ui/sonner'
import ImageLightbox from './components/preview/ImageLightbox.vue'
</script>

<template>
  <AppLayout>
    <router-view />
    <Toaster />
    <ImageLightbox />
  </AppLayout>
</template>
