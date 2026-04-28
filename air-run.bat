@echo off
set DATABASE_URL=postgresql://genfity:dbgenfity2026@localhost:5432/genfity_ai_gateway?sslmode=disable
set REDIS_URL=redis://localhost:6379/3
set GENFITY_INTERNAL_SECRET=genfity-ai-gateway-internal-dev
set AI_ROUTER_CORE2_INTERNAL_URL=http://localhost:8317
set AI_ROUTER_CORE2_API_KEY=your-api-key-1
air
