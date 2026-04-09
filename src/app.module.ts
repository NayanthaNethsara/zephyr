import { Module } from '@nestjs/common';
import { ConfigModule } from '@nestjs/config';
import { WebhookModule } from './webhook/webhook.module';
import { WhatsAppModule } from './whatsapp/whatsapp.module';
import { HealthModule } from './health/health.module';
import { NatsModule } from './nats/nats.module';
import configuration from './config/configuration';
import { validationSchema } from './config/validation';
import { PinoAppLogger } from './common/logger';

@Module({
  imports: [
    PinoAppLogger.registerAsync({
      logDestination: 'logs/app.log',
    }),
    ConfigModule.forRoot({
      isGlobal: true,
      load: [configuration],
      validationSchema,
      validationOptions: {
        allowUnknown: true,
        abortEarly: false,
      },
      envFilePath: '.env',
    }),
    WhatsAppModule,
    WebhookModule,
    HealthModule,
    NatsModule,
  ],
})
export class AppModule {}
