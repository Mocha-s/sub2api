export default {
  taskLogs: {
    title: 'Task Logs',
    description: 'Request status of image and video tasks',
    allTypes: 'All types',
    allStatuses: 'All statuses',
    userId: 'User ID',
    empty: 'No tasks yet',
    batchItems: '{total} images ({success} succeeded)',
    preview: 'Preview',
    download: 'Download',
    view: 'View',
    copyTaskId: 'Click to copy task ID',
    detailTitle: 'Task Details',
    prompt: 'Prompt',
    taskName: 'Task name',
    error: 'Error',
    rawStatus: 'Raw status',
    loadFailed: 'Failed to load task logs',
    resultFailed: 'Failed to fetch result',
    columns: {
      submittedAt: 'Submitted',
      finishedAt: 'Finished',
      duration: 'Duration',
      platform: 'Platform',
      type: 'Type',
      taskId: 'Task ID',
      status: 'Status',
      progress: 'Progress',
      user: 'User',
      summary: 'Summary',
      detail: 'Details'
    },
    types: {
      video: 'Video',
      batch_image: 'Batch image'
    },
    status: {
      queued: 'Queued',
      running: 'Running',
      succeeded: 'Succeeded',
      failed: 'Failed',
      cancelled: 'Cancelled',
      expired: 'Timed out'
    }
  }
}
