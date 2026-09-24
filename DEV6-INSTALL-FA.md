# نصب مرحله‌ای dev.6

این نسخه دو بخش دارد و ترتیب نصب مهم است.

## بخش اول: GitHub

محتویات پوشه `helper-dev6` را جایگزین فایل‌های هم‌نام مخزن GitHub کنید. منظور از
«جایگزین» این است که فایل جدید روی فایل هم‌نام قبلی نوشته شود؛ لازم نیست ابتدا
کل مخزن یا فایل‌های قدیمی را حذف کنید.

پس از Commit، منتظر بمانید تا GitHub Actions شامل تست و ساخت image با موفقیت
تمام شود. پیام پیشنهادی Commit:

`feat: add profile activation and RouterOS mode control`

## بخش دوم: MikroTik

1. فایل `VLESS-Mode-Manager-v0.2.rsc` را در Files روتر بارگذاری کنید.
2. اگر OpenVPN پیش‌فرض شما `NetXN` نیست، قبل از Import مقدار `ovpnName` داخل
   فایل را به نام اتصال موردنظر تغییر دهید.
3. فایل را Import کنید:

```routeros
/import file-name=VLESS-Mode-Manager-v0.2.rsc
```

4. خروجی زیر را بررسی کنید:

```routeros
/system/script print where name="VLESS-Mode-Manager"
/system/scheduler print detail where name~"VLESS-Mode"
/system/script/run VLESS-Mode-Manager
/file print detail where name="helper-data/router-status.json"
```

5. فقط پس از موفقیت مرحله‌های بالا، image جدید Helper را با root جدید نصب کنید.
   کانتینر dev.5 را تا زمان تأیید dev.6 حذف نکنید.

## آزمون پذیرش

- Prepare نباید Xray را restart کند.
- Activate باید پروفایل را اعمال کند و ردیف آن به Active تغییر کند.
- Disable VLESS باید حالت BLOCKED ایجاد کند، نه دسترسی مستقیم.
- Enable VLESS باید Xray و tun2socks و مسیر VLESS را فعال کند.
- Direct WAN فقط پس از تأیید کاربر فعال شود.
- پس از reboot، حدود 45 ثانیه برای بازیابی سرویس فرصت دهید و سپس IP خروجی را
  بررسی کنید.
