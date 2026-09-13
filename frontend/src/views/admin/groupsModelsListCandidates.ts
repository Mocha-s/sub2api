import type { GroupPlatform } from '@/types'
export type ModelsListCandidatesMode = 'create' | 'edit'
export interface ModelsListCandidatesRequest { mode: ModelsListCandidatesMode; groupID: number; platform: GroupPlatform }
export interface ModelsListCandidatesTracker { next(request: ModelsListCandidatesRequest): number; isCurrent(requestID: number, request: ModelsListCandidatesRequest): boolean }
export const createModelsListCandidatesTracker = (): ModelsListCandidatesTracker => { let id = 0; const current: Partial<Record<ModelsListCandidatesMode, { id: number; request: ModelsListCandidatesRequest }>> = {}; return { next(request) { id += 1; current[request.mode] = { id, request: { ...request } }; return id }, isCurrent(requestID, request) { const value = current[request.mode]; return value?.id === requestID && value.request.groupID === request.groupID && value.request.platform === request.platform } } }
