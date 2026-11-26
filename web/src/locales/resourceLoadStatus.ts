export const resourceLoadStatusZh = {
  title: '同步状态', failed: '读取失败 {count}', retryFailed: '重试失败项', retry: '重试', retrying: '重试中',
  noErrors: '当前没有读取错误', pending: '部分数据仍在读取中', separator: '、',
}
export const resourceLoadStatusEn: Record<keyof typeof resourceLoadStatusZh, string> = {
  title: 'Sync status', failed: '{count} read errors', retryFailed: 'Retry failed', retry: 'Retry', retrying: 'Retrying',
  noErrors: 'No current read errors', pending: 'Some data is still loading', separator: ', ',
}
