# Backend'i Başlatma

Backend'i Docker kullanarak başlatmak için aşağıdaki adımları uygulayın.

## 1. Docker imajını oluşturun

Proje klasöründe terminali açın ve şu komutu çalıştırın:

```bash
docker build -t gb-go .
```

## 2. Backend container'ını başlatın

```bash
docker run --rm -p 3000:3000 gb-go
```

Backend şu adreste çalışır:

```text
http://localhost:3000
```

## 3. Container'ı durdurun

Backend'i durdurmak için terminalde `Ctrl+C` tuşlarına basın.

## API endpoint'leri

Mevcut authentication endpoint'leri:

- `POST /api/register`
- `POST /api/login`

Bu endpoint'ler şu anda temel şablon olarak boş bırakılmıştır.

> Not: openGauss şu anda başlatılmamaktadır. Veritabanı bağlantısı daha sonra etkinleştirilecektir.
