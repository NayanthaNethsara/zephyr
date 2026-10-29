import { DynamicModule, Module } from '@nestjs/common';
import { ConfigModule, ConfigService } from '@nestjs/config';
import { LoggerModule } from 'nestjs-pino';

function serializeRequest(req: { id?: string; method?: string; url?: string }) {
  return { id: req.id, method: req.method, path: req.url?.split('?')[0] };
}

function serializeResponse(res: { statusCode?: number }) {
  return { statusCode: res.statusCode };
}

@Module({})
export class PinoAppLogger {
  static register(): DynamicModule {
    return {
      global: true,
      module: PinoAppLogger,
      imports: [
        LoggerModule.forRootAsync({
          imports: [ConfigModule],
          inject: [ConfigService],
          useFactory: (config: ConfigService) => {
            const production = config.get<string>('NODE_ENV') === 'production';
            return {
              pinoHttp: {
                level: config
                  .get<string>('LOG_LEVEL', production ? 'info' : 'debug')
                  .toLowerCase(),
                serializers: { req: serializeRequest, res: serializeResponse },
                base: { service: 'whatsapp-gateway' },
              },
            };
          },
        }),
      ],
      exports: [LoggerModule],
    };
  }
}
