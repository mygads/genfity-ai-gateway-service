@echo off
setlocal

if "%DATABASE_URL%"=="" set "DATABASE_URL=postgresql://genfity:dbgenfity2026@localhost:5432/genfity_ai_gateway?sslmode=disable"
set "GOOSE_ATTEMPT=1"

:migrate
goose -dir internal/database/migrations postgres "%DATABASE_URL%" up
if %errorlevel% equ 0 goto build
if %GOOSE_ATTEMPT% geq 30 exit /b %errorlevel%
set /a WAIT_SECONDS=GOOSE_ATTEMPT*2
if %WAIT_SECONDS% gtr 30 set "WAIT_SECONDS=30"
echo goose migration failed, retrying in %WAIT_SECONDS%s... attempt %GOOSE_ATTEMPT%/30
timeout /t %WAIT_SECONDS% /nobreak >NUL
set /a GOOSE_ATTEMPT+=1
goto migrate

:build
go build -o tmp\genfity-ai-gateway.exe .\cmd\http
