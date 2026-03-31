<template>
  <div class="card p-6">
    <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.settings.data.title') }}</h3>
    <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">
      已移除 Sora 存储相关功能，当前仅保留通用数据管理能力。
    </p>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
const { t } = useI18n()
</script>
