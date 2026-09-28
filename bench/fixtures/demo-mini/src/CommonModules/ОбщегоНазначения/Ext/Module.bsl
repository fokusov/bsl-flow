// CommonModule: ОбщегоНазначения (server, shared helpers).
//
// ПроверитьПревышениеЛимита has a known off-by-comparison bug (task s-bugfix-common-module):
// a sum exactly equal to the limit must NOT count as exceeding it (only Сумма > Лимит
// should), but the current "<" comparison makes an equal sum count as exceeding.

Функция ПроверитьПревышениеЛимита(Сумма, Лимит) Экспорт

	Если Сумма < Лимит Тогда
		Возврат Ложь;
	КонецЕсли;

	Возврат Истина;

КонецФункции

Функция ПривестиКСтроке(Значение) Экспорт

	Возврат Строка(Значение);

КонецФункции

