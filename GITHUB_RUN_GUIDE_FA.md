# راهنمای اجرای نسخهٔ توسعه‌ای در GitHub

## فایل‌هایی که باید در مخزن قرار گیرند

تمام محتویات پوشهٔ `vless-helper` باید با همین ساختار در ریشهٔ مخزن GitHub قرار گیرند.

هیچ‌یک از فایل‌های Go را جداگانه در GitHub اجرا نکنید. فایل Workflow مراحل لازم را خودکار انجام می‌دهد.

## اجرای خودکار

با Push کردن فایل‌ها روی شاخهٔ `main`، Workflow زیر اجرا می‌شود:

```text
.github/workflows/container.yml
```

ترتیب اجرا:

1. `go vet ./...`
2. `go test ./...`
3. `go test -race ./...`
4. ساخت image برای `linux/arm/v7`، `linux/arm64` و `linux/amd64`
5. انتشار image در GitHub Container Registry

اگر یکی از سه مرحلهٔ بررسی شکست بخورد، image منتشر نمی‌شود.

## اجرای دستی

در صفحهٔ مخزن:

1. وارد بخش `Actions` شوید.
2. Workflow با نام `Build multi-arch container` را انتخاب کنید.
3. روی `Run workflow` بزنید.
4. شاخهٔ `main` را انتخاب کنید.
5. اجرای Workflow را تأیید کنید.

## نکتهٔ مهم

تا زمانی که هر دو Job با نام‌های `test` و `build` سبز نشده‌اند، image را روی RouterOS نصب نکنید.
