import { request } from './client'
import type {
  PushSubscriptionInput,
  PushSubscriptionsResp,
  PushSubscriptionView,
} from './types'

export function getVAPIDPublicKey(): Promise<{ public_key: string }> {
  return request<{ public_key: string }>('/v1/push/vapid-public-key')
}

export function listPushSubscriptions(): Promise<PushSubscriptionsResp> {
  return request<PushSubscriptionsResp>('/v1/push/subscriptions')
}

export function createPushSubscription(
  input: PushSubscriptionInput,
): Promise<PushSubscriptionView> {
  return request<PushSubscriptionView>('/v1/push/subscriptions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export function deletePushSubscription(endpoint: string): Promise<{ deleted: boolean }> {
  return request<{ deleted: boolean }>('/v1/push/subscriptions', {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ endpoint }),
  })
}

export function sendTestPush(): Promise<{ queued: number }> {
  return request<{ queued: number }>('/v1/push/test', { method: 'POST' })
}
