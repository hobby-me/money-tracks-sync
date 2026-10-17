# Строка контрольной суммы: fmt.Sprintf vs append

Горячий путь `/v1/checksum`: на каждую строку `rows_state` строится
`uuid:hlc:device_id\n` и скармливается SHA-256. Кандидаты — `fmt.Fprintf`
в хэш против `append` в переиспользуемый буфер (`store.ChecksumLine`).

10 000 строк на итерацию, `-benchmem -count=3`, Apple M4 Pro, go1.27.0:

| Bench                       | ns/op   | B/op    | allocs/op |
|-----------------------------|---------|---------|-----------|
| ChecksumLineSprintf         | 873–901k| 480 586 | 30 002    |
| ChecksumLineAppend          | 256–260k| 288     | 3         |

`fmt` тратит ~3,4× времени и по три аллокации на строку
(interface-boxing аргументов + парсинг формат-строки). Выбран `append` —
`store.ChecksumLine`, буфер переживает весь проход.
