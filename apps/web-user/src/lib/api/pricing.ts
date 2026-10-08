import { client } from '../api'

export type ModelPriceVersion = {
  id: number
  priceVersion: string
  connectionId: number
  endpointFingerprint: string
  modelName: string
  currency: string
  inputPriceMinorPerMillion: number
  outputPriceMinorPerMillion: number
  isFree: boolean
  isEstimated: boolean
  effectiveFrom: string
  note: string
}

export type ModelPriceInput = Pick<ModelPriceVersion, 'priceVersion' | 'inputPriceMinorPerMillion' | 'outputPriceMinorPerMillion' | 'isFree' | 'isEstimated' | 'note'>

export const modelPriceApi = {
  get: (providerId: number) => client.get<{ price: ModelPriceVersion | null }>(`/v1/settings/model-prices/${providerId}`).then((response) => response.data.price),
  save: (providerId: number, input: ModelPriceInput) => client.put<{ price: ModelPriceVersion }>(`/v1/settings/model-prices/${providerId}`, input).then((response) => response.data.price),
}
