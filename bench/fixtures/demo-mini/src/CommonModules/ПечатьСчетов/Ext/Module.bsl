// CommonModule: ПечатьСчетов (server, print-forms helper).
//
// Existing behaviour: builds the printable spreadsheet document for an invoice. Does not
// yet render a scannable payment code, which is the target of task s-print-form (see
// bench/tasks/s-print-form/original-task.md for what to add).

Функция СформироватьПечатнуюФорму(Основание) Экспорт

	ТабДок = Новый ТабличныйДокумент;
	Макет = ПолучитьМакет("Печать");
	ОбластьЗаголовок = Макет.ПолучитьОбласть("Заголовок");
	ТабДок.Вывести(ОбластьЗаголовок);

	ОбластьСтрока = Макет.ПолучитьОбласть("Строка");
	Для Каждого СтрокаТоваров Из Основание.Товары Цикл
		ОбластьСтрока.Параметры.Заполнить(СтрокаТоваров);
		ТабДок.Вывести(ОбластьСтрока);
	КонецЦикла;

	Возврат ТабДок;

КонецФункции

