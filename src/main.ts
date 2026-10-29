import { NestFactory } from '@nestjs/core';
import { AppModule } from './app.module';
import {
  FastifyAdapter,
  NestFastifyApplication,
} from '@nestjs/platform-fastify';
import { ConfigService } from '@nestjs/config';
import { HttpExceptionFilter } from './common/filters/http-exception.filter';
import helmet from '@fastify/helmet';
import compression from '@fastify/compress';
import { Logger } from 'nestjs-pino';
import { randomUUID } from 'node:crypto';

async function bootstrap() {
  const fastifyAdapter = new FastifyAdapter({
    bodyLimit: 10485760,
    genReqId: () => randomUUID(),
  });
  fastifyAdapter.getInstance().addHook('onRequest', async (request, reply) => {
    reply.header('X-Request-ID', request.id);
  });

  const app = await NestFactory.create<NestFastifyApplication>(
    AppModule,
    fastifyAdapter,
    {
      rawBody: true,
    },
  );
  const configService = app.get(ConfigService);

  app.useLogger(app.get(Logger));
  app.enableShutdownHooks();

  app.useGlobalFilters(new HttpExceptionFilter());

  await app.register(helmet);
  await app.register(compression);

  const port = configService.getOrThrow<number>('port');
  await app.listen(port, '0.0.0.0');
}

void bootstrap();
