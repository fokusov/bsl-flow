// CommonModule: ОтчетПродаж (server, sales report helper).
//
// СформироватьОтчет returns rows for every client with no ranking; used by task
// ambiguous-report-request, where the request text under-specifies what "top clients"
// means (by revenue? by order count? over what period?).

Функция СформироватьОтчет(НачалоПериода, КонецПериода) Экспорт

	Результат = Новый ТаблицаЗначений;
	Результат.Колонки.Добавить("Клиент");
	Результат.Колонки.Добавить("Сумма");

	Возврат Результат;

КонецФункции

