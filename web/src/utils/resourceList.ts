import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

export function useResourceList<T extends { id: string }>(rows: () => T[], requestedID: () => string = () => '') {
  const { t } = useI18n()
  const detailID = ref('')
  const detailOpen = ref(false)
  const pageState = reactive({ current: 1, pageSize: 10 })
  const selected = computed(() => rows().find(row => row.id === detailID.value))
  const pagination = computed(() => ({ ...pageState, showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'], showTotal: (count: number) => t('planList.total', { count }) }))
  let revealedID = ''

  function openDetails(row: T) { detailID.value = row.id; detailOpen.value = true }
  function changePage(page: { current?: number; pageSize?: number }) {
    const size = page.pageSize || pageState.pageSize
    pageState.current = size === pageState.pageSize ? page.current || 1 : 1
    pageState.pageSize = size
  }
  watch(() => ({ ids: rows().map(row => row.id), requested: requestedID() }), ({ ids, requested }) => {
    pageState.current = Math.min(pageState.current, Math.max(1, Math.ceil(ids.length / pageState.pageSize)))
    if (detailID.value && !ids.includes(detailID.value)) { detailOpen.value = false; detailID.value = '' }
    if (!requested) revealedID = ''
    else if (requested !== revealedID && ids.includes(requested)) {
      pageState.current = Math.floor(ids.indexOf(requested) / pageState.pageSize) + 1
      detailID.value = requested
      detailOpen.value = true
      revealedID = requested
    }
  }, { immediate: true })
  return { detailID, detailOpen, selected, pageState, pagination, openDetails, changePage }
}
