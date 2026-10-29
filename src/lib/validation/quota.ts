import { z } from 'zod';

export const retryAfterSchema = z
  .string()
  .regex(/^[1-9]\d{0,4}$/)
  .transform(Number)
  .pipe(z.number().int().min(1).max(86400))
  .catch(60);
