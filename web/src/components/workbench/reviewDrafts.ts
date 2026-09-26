export const REVIEW_DRAFT_STORAGE_KEY = 'gofer.workbench.review-drafts.v1'

export type ReviewDraftSide = 'new' | 'old'

export interface ReviewDraftComment {
  id: string
  path: string
  line: number
  side: ReviewDraftSide
  text: string
}

export interface ThreadReviewDraft {
  summary: string
  comments: ReviewDraftComment[]
}

export type ReviewDraftState = Record<string, ThreadReviewDraft>

export interface ReviewRequestComment {
  path: string
  line: number
  side: ReviewDraftSide
  text: string
}

export interface ReviewRequestBody {
  summary: string
  comments: ReviewRequestComment[]
}

export function createReviewDraftState(): ReviewDraftState {
  return {}
}

export function reviewDraftFor(state: Readonly<ReviewDraftState>, threadId: string): ThreadReviewDraft {
  const draft = state[threadId]
  if (!draft) return { summary: '', comments: [] }
  return {
    summary: draft.summary,
    comments: draft.comments.map((comment) => ({ ...comment })),
  }
}

export function setReviewSummary(
  state: Readonly<ReviewDraftState>,
  threadId: string,
  summary: string,
): ReviewDraftState {
  const draft = reviewDraftFor(state, threadId)
  return {
    ...state,
    [threadId]: { ...draft, summary },
  }
}

export function addReviewComment(
  state: Readonly<ReviewDraftState>,
  threadId: string,
  comment: ReviewDraftComment,
): ReviewDraftState {
  const draft = reviewDraftFor(state, threadId)
  const comments = draft.comments.filter((candidate) => candidate.id !== comment.id)
  comments.push({ ...comment })
  return {
    ...state,
    [threadId]: { ...draft, comments },
  }
}

export function updateReviewComment(
  state: Readonly<ReviewDraftState>,
  threadId: string,
  commentId: string,
  text: string,
): ReviewDraftState {
  const existing = state[threadId]
  if (!existing || !existing.comments.some((comment) => comment.id === commentId)) return state as ReviewDraftState
  return {
    ...state,
    [threadId]: {
      summary: existing.summary,
      comments: existing.comments.map((comment) => (
        comment.id === commentId ? { ...comment, text } : { ...comment }
      )),
    },
  }
}

export function removeReviewComment(
  state: Readonly<ReviewDraftState>,
  threadId: string,
  commentId: string,
): ReviewDraftState {
  const existing = state[threadId]
  if (!existing || !existing.comments.some((comment) => comment.id === commentId)) return state as ReviewDraftState
  return {
    ...state,
    [threadId]: {
      summary: existing.summary,
      comments: existing.comments
        .filter((comment) => comment.id !== commentId)
        .map((comment) => ({ ...comment })),
    },
  }
}

export function clearReviewDraft(
  state: Readonly<ReviewDraftState>,
  threadId: string,
): ReviewDraftState {
  if (!(threadId in state)) return state as ReviewDraftState
  const next = { ...state }
  delete next[threadId]
  return next
}

export function buildReviewRequest(
  state: Readonly<ReviewDraftState>,
  threadId: string,
): ReviewRequestBody {
  const draft = reviewDraftFor(state, threadId)
  return {
    summary: draft.summary.trim(),
    comments: draft.comments.map(({ path, line, side, text }) => ({
      path: path.trim(),
      line,
      side,
      text: text.trim(),
    })),
  }
}

export function normalizeReviewDraftState(value: unknown): ReviewDraftState {
  if (!isRecord(value)) return {}
  const state: ReviewDraftState = {}
  for (const [threadId, rawDraft] of Object.entries(value)) {
    if (!threadId || !isRecord(rawDraft) || typeof rawDraft.summary !== 'string' || !Array.isArray(rawDraft.comments)) {
      continue
    }
    const comments: ReviewDraftComment[] = []
    for (const rawComment of rawDraft.comments) {
      if (!isRecord(rawComment)) continue
      if (
        typeof rawComment.id !== 'string'
        || typeof rawComment.path !== 'string'
        || typeof rawComment.line !== 'number'
        || !Number.isInteger(rawComment.line)
        || rawComment.line <= 0
        || (rawComment.side !== 'new' && rawComment.side !== 'old')
        || typeof rawComment.text !== 'string'
      ) {
        continue
      }
      comments.push({
        id: rawComment.id,
        path: rawComment.path,
        line: rawComment.line,
        side: rawComment.side,
        text: rawComment.text,
      })
    }
    state[threadId] = { summary: rawDraft.summary, comments }
  }
  return state
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
