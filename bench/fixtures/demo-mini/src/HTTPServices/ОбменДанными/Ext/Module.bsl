// HTTPService.ОбменДанными module.
//
// Task l-exchange-http-service: add a new POST handler that accepts an order payload and
// forwards it to the exchange queue, without changing the existing GET status handler below.

Функция ПолучитьСтатус(Запрос)

	Ответ = Новый HTTPServiceResponseWriter();
	Ответ.УстановитьТелоИзСтроки("{""status"":""ok""}");
	Возврат Ответ;

КонецФункции

