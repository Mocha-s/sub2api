export default {
  taskLogs: {
    title: '任务日志',
    description: '查看图片与视频任务的请求状态',
    allTypes: '全部类型',
    allStatuses: '全部状态',
    userId: '用户 ID',
    empty: '暂无任务',
    batchItems: '{total} 张（{success} 成功）',
    imageItems: '{total} 张（{success} 成功）',
    requestModes: { sync: '同步', async: '异步' },
    preview: '预览',
    previousImage: '上一张',
    download: '下载',
    view: '查看',
    copyTaskId: '点击复制任务 ID',
    detailTitle: '任务详情',
    prompt: '提示词',
    taskName: '任务名',
    error: '错误',
    rawStatus: '原始状态',
    loadFailed: '加载任务日志失败',
    resultFailed: '获取结果失败',
    columns: {
      submittedAt: '提交时间',
      finishedAt: '结束时间',
      duration: '花费时间',
      platform: '平台',
      type: '类型',
      taskId: '任务ID',
      status: '任务状态',
      progress: '进度',
      user: '用户',
      summary: '明细',
      detail: '详情'
    },
    types: {
      video: '视频',
      image: '图片',
      batch_image: '批量图片'
    },
    status: {
      queued: '排队中',
      running: '进行中',
      succeeded: '成功',
      failed: '失败',
      cancelled: '已取消',
      expired: '已超时'
    }
  }
}
