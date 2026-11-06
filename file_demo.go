package main

import (
	"fmt"
	"strconv"
	"strings"
)

func fileDemoProblems() []ProblemSpec {
	var out []ProblemSpec
	titles := []string{"Сумма из текстового файла", "Контрольная сумма бинарного файла", "Инверсия байтов", "Числа в бинарный файл"}
	statements := []string{
		"Прочитайте целые числа из файла `numbers.txt` и запишите их сумму в файл `sum.txt`. Числа разделены пробелами или переводами строк. Пустой файл означает сумму 0.",
		"Прочитайте все байты файла `data.bin`. Выведите в стандартный вывод сумму значений байтов (каждый байт — число от 0 до 255). Для пустого файла выведите 0.",
		"Прочитайте файл `source.bin`. Создайте файл `result.bin`: каждый байт x замените на 255 − x, сохранив порядок. Для пустого входа создайте пустой выходной файл.",
		"Прочитайте числа от 0 до 255 из текстового файла `values.txt`. Создайте `packed.bin`, записав каждое число ровно одним байтом. Не записывайте текстовые цифры, пробелы или перевод строки. Для пустого входа создайте пустой файл.",
	}
	for i := 0; i < 4; i++ {
		p := defaultProblem()
		p.FormatVersion = 2
		p.Code = strconv.Itoa(i + 1)
		p.Title = titles[i]
		p.Statement = statements[i]
		p.Examples = nil
		p.Tests = nil
		p.InputFormat = "Содержимое указанного в условии файла. Размер — до 1 МБ."
		p.OutputFormat = "Ответ в источнике, указанном в условии."
		p.Notes = "Файл находится в текущей рабочей папке. Сдайте только исходный код. Python: open(..., 'rb'/'wb') для .bin; C++: ios::binary. Для бинарных примеров байты записаны в HEX."
		p.IO.Input = IOChannel{Type: "file", Content: "text"}
		p.IO.Output = IOChannel{Type: "file", Content: "text"}
		switch i {
		case 0:
			p.IO.Input.Name = "numbers.txt"
			p.IO.Output.Name = "sum.txt"
			p.Checker.Type = "tokens"
			p.Examples = []Example{{"2 3\n", "5\n", "numbers.txt → sum.txt"}}
		case 1:
			p.IO.Input = IOChannel{"file", "data.bin", "binary"}
			p.IO.Output = IOChannel{}
			p.Checker.Type = "tokens"
			p.Examples = []Example{{"00 01 ff", "256\n", "HEX содержимого data.bin → stdout"}}
		case 2:
			p.IO.Input = IOChannel{"file", "source.bin", "binary"}
			p.IO.Output = IOChannel{"file", "result.bin", "binary"}
			p.Checker.Type = "bytes"
			p.Examples = []Example{{"00 01 ff", "ff fe 00", "HEX: source.bin → result.bin"}}
		case 3:
			p.IO.Input.Name = "values.txt"
			p.IO.Output = IOChannel{"file", "packed.bin", "binary"}
			p.Checker.Type = "bytes"
			p.Examples = []Example{{"0 1 255\n", "00 01 ff", "values.txt → packed.bin (HEX)"}}
		}
		inputs := [][]byte{{}, {0, 1, 255}, {10, 13, 0, 128, 254}, {42}, make([]byte, 256)}
		for j := range inputs[4] {
			inputs[4][j] = byte(j)
		}
		for j, b := range inputs {
			t := TestCase{ID: fmt.Sprintf("%02d", j+1), Points: 20}
			sum := 0
			words := []string{}
			for _, v := range b {
				sum += int(v)
				words = append(words, strconv.Itoa(int(v)))
			}
			if p.IO.Input.Binary() {
				t.InputBytes = append([]byte{}, b...)
			} else {
				t.Input = strings.Join(words, " ")
			}
			switch i {
			case 0, 1:
				t.Answer = fmt.Sprintf("%d\n", sum)
			case 2:
				t.AnswerBytes = make([]byte, len(b))
				for k, v := range b {
					t.AnswerBytes[k] = 255 - v
				}
			case 3:
				t.AnswerBytes = append([]byte{}, b...)
			}
			p.Tests = append(p.Tests, t)
		}
		out = append(out, p)
	}
	return out
}
