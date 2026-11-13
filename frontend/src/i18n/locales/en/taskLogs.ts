export default {
  taskLogs: {
    title: 'Task Logs',
    description: 'Request status of image and video tasks',
    allTypes: 'All types',
    allStatuses: 'All statuses',
    userId: 'User ID',
    empty: 'No tasks yet',
    batchItems: '{total} images ({success} succeeded)',
    imageItems: '{total} images ({success} succeeded)',
    requestModes: { sync: 'Synchronous', async: 'Asynchronous' },
    preview: 'Preview',
    videoPreview: 'Video preview',
    imagePreview: 'Image preview',
    loadingMedia: 'Loading media. Large files may take a moment…',
    downloading: 'Downloading…',
    downloadStarted: 'Download handed to your browser. Check your downloads.',
    playbackFailed: 'Your browser cannot play this video. Download it and use a local player.',
    previousImage: 'Previous image',
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
      image: 'Image',
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
